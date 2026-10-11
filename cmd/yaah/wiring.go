package yaah

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"

	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/runner"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	processpkg "github.com/buchenberg/yaah/internal/process"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func newAgentSession() (*agentSession, error) {
	return newAgentSessionWithOptions(sessionOptionsFromFlags(), false, false)
}

// defaultWorkspaceDenyPatterns blocks common secret files inside the
// workspace when agents.default.workspace_deny_patterns is unset
// (review finding S6). An explicit config list overrides this; an
// explicit empty list disables deny patterns entirely.
var defaultWorkspaceDenyPatterns = []string{
	".env",
	".env.*",
	"*.pem",
	"*.key",
	"id_rsa*",
	"id_ed25519*",
	"id_ecdsa*",
}

// workspaceSandboxStartTimeout bounds the isolated workspace's startup —
// the D5 image probe (its own throwaway sandbox), the session sandbox's
// Create, the D1 copy-in (one backend exec per file), and the baseline
// capture/rebind — so a wedged containerd daemon fails the session at
// startup instead of hanging it. Generous: a first-run image pull and a
// large working tree both happen inside this window.
const workspaceSandboxStartTimeout = 15 * time.Minute

// toolSpillDir is the single source for the tool-result spill directory.
// Both the parent loop (build_loop.go) and every sub-agent loop inherit
// it so oversized tool results land in one place. Sub-agents receive it
// via TaskToolOpts.ToolSpillDir — internal/agent never reads config
// paths directly (finding C4).
func toolSpillDir() string {
	return filepath.Join(config.HomeDir(), "truncated")
}

// newAgentSessionWithOptions creates an agent session. All CLI-flag and
// serve-mode inputs arrive via opts — no hidden package state. When
// skipMCP is true, MCP server subprocesses are not started (they consume
// CPU and make tview input sluggish on Windows). When skipOtel is true,
// OTel exporters are not initialised.
func newAgentSessionWithOptions(opts SessionOptions, skipMCP, skipOtel bool) (*agentSession, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if verr := config.Validate(cfg); verr != nil {
		return nil, fmt.Errorf("config: %w", verr)
	}

	provider := resolveProvider(cfg)
	modelName := resolveModel(cfg)
	providerName := resolveProviderName(cfg)

	// --- OpenTelemetry ---------------------------------------------------
	otelShutdown, otelActive, err := initOtel(cfg, opts, skipOtel)
	if err != nil {
		return nil, err
	}

	// Startup ownership: every resource this function creates from here on
	// is ours until the returned session takes it over. Any error return
	// must close what was opened — a failed startup that leaves OTel
	// exporters, MCP subprocesses, or a SQLite handle alive accumulates
	// across retries in serve mode. sessionHandedOff flips only on the
	// success path; the closer below runs on every other return.
	//
	// db and mcpClients are declared here (not at their call sites) so the
	// guard can close a partially-built session no matter which later step
	// failed.
	var (
		db           *memory.DB
		mcpClients   []mcp.MCPClient
		mcpInfos     []mcp.ServerInfo
		mcpToolNames map[string]bool
		// supervisorSvc is the advisory rules service (adoption plan
		// B1). Declared here, like db, so the guard can close a
		// partially-built session no matter which later step failed —
		// a started service owns a drain goroutine and a bus
		// subscription, and a failed startup must not leak either.
		supervisorSvc *supervision.Service
	)
	sessionHandedOff := false
	defer func() {
		if sessionHandedOff {
			return
		}
		if supervisorSvc != nil {
			supervisorSvc.Close()
		}
		if otelShutdown != nil {
			otelShutdown(context.Background())
		}
		for _, c := range mcpClients {
			c.Close()
		}
		if db != nil {
			db.Close()
		}
	}()

	// A failed Getwd must not limp on as an empty cwd: the isolated
	// bootstrap reads "" as "no source requested" and would silently skip
	// the copy-in, starting the containerd session on an empty container
	// with no baseline.
	cwd, gerr := os.Getwd()
	if gerr != nil {
		return nil, fmt.Errorf("determine the working directory: %w", gerr)
	}

	// Load sub-agent role definitions: built-in (embedded) +
	// user-defined (~/.agents/roles/, ./.agents/roles/).
	reg := subagent.NewRoleRegistry()
	if files := runner.BuiltinRoleFiles(); files != nil {
		reg.LoadBytes(files)
	}
	for _, dir := range runner.RoleSearchPaths(cwd) {
		reg.LoadDir(dir)
	}
	subagent.SetDefaultRoleRegistry(reg)

	// The tool registry (plan §3, Option A): local mode installs every
	// built-in; isolated mode curates the set — the host-only leaf tools
	// are left out here, the runtime-wired host-only tools at their
	// registration sites below — so Registry.SetWorkspace's gate finds
	// both diagnostic lists empty and the isolated session can actually
	// be selected. The model is told about the delta (plan §2.2) and the
	// record keeps it (decision D4).
	isolated := cfg.Workspace.Backend == config.BackendContainerd
	var omittedTools []string
	var toolReg *tools.Registry
	if isolated {
		toolReg = tools.NewIsolatedRegistry()
		// Derived from the same hostOnly() marker the registry filter
		// and the SetWorkspace gate read, so the prompt delta and the
		// D4 record can never name a tool the session actually has, or
		// miss one it does not.
		omittedTools = append(omittedTools, tools.HostOnlyLeafTools()...)
	} else {
		toolReg = tools.NewRegistry()
	}

	// Workspace containment. Exactly one substrate may be installed:
	//
	//   - containerd backend: the session's sandbox is constructed, image-
	//     probed (D5), and created here, once per session. SetWorkspace
	//     (after every registration, below) hands the registry the sandbox
	//     workspace. The host path validator must NOT also run — it would
	//     install a local workspace over the registry's nil one, and the
	//     two substrates must not race for tools.
	//   - local backend with --workspace: the host validator gates paths.
	//   - neither: no validator; legacy ~-expansion behaviour.
	var pathValidator *tools.PathValidator
	var sessionSandbox *tools.SessionSandbox
	// sandboxTeardownHandled flips when the sandbox is either handed to the
	// session (success path — close() destroys it) or already destroyed by
	// an error branch below; the guard exists so no error return leaks a
	// container, and so one failure is never reported on two channels.
	sandboxTeardownHandled := false
	// Deny patterns: explicit config wins; unset falls back to the
	// built-in secret-file defaults; an explicit empty list disables. Both
	// substrates resolve the same list — the local validator gates tool
	// access, and the isolated bootstrap excludes matching files from the
	// copy-in so they never become agent-readable inside the container
	// either.
	deny := cfg.Agent.Default.WorkspaceDenyPatterns
	if deny == nil {
		deny = defaultWorkspaceDenyPatterns
	}
	switch {
	case cfg.Workspace.Backend == config.BackendContainerd:
		// A wedged daemon must not hang session startup: the probe, the
		// Create, and the contract exec all run under one generous
		// bound (large enough for a first-run image pull).
		startCtx, cancelStart := context.WithTimeout(context.Background(), workspaceSandboxStartTimeout)
		defer cancelStart()
		sessionSandbox, err = tools.StartSessionSandbox(
			startCtx, cfg.Workspace, tools.OpenContainerdWorkspaceSandbox, cwd, deny)
		if err != nil {
			return nil, err
		}
		// Every error return below destroys the sandbox instead of
		// leaking a container for the lifetime of the daemon's namespace
		// — bounded, like every other teardown, so a wedged daemon cannot
		// also hang a failed startup on its way out.
		defer func() {
			if sessionSandbox != nil && !sandboxTeardownHandled {
				destroyCtx, cancelDestroy := context.WithTimeout(context.Background(), workspaceSandboxDestroyTimeout)
				defer cancelDestroy()
				if derr := sessionSandbox.Destroy(destroyCtx); derr != nil {
					fmt.Fprintf(os.Stderr, "warning: workspace sandbox teardown after failed session start: %v\n", derr)
				}
			}
		}()
		if opts.WorkspaceRoot != "" {
			fmt.Fprintf(os.Stderr, "warning: --workspace %s has no effect: the workspace.backend containerd session is isolated from the host\n", opts.WorkspaceRoot)
		}
	case opts.WorkspaceRoot != "":
		pathValidator = tools.NewPathValidator(opts.WorkspaceRoot, opts.AllowHomeAccess, deny)
		toolReg.SetPathValidator(pathValidator)
	}

	// The accept seam (plan §2.3, D1): the single door changes leave the
	// container through, registered only in isolated mode — in local mode
	// the tools write the host directly and there is nothing to let out.
	// Registered before buildMainPrompt so the quick-ref card includes it.
	if sessionSandbox != nil {
		toolReg.Register(&tools.ApplyOutTool{Sandbox: sessionSandbox, HostDir: cwd})
	}

	db, err = memory.OpenDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: db open failed, persistence disabled: %v\n", err)
		db = nil
	}

	// --- Embedding server ------------------------------------------------
	// The embedder is attached here (skillTool reads it below), but the
	// backfill goroutine launches only after the last fallible step
	// (the SetWorkspace gate, further down): the startup guard closes db
	// on every error return, and a backfill racing that close aborts
	// under a spurious warning.
	embedderAttached := false
	if db != nil {
		embedderAttached = attachEmbedder(db, cfg)
	}

	// --- Prompt assembly -------------------------------------------------
	systemPrompt := buildSystemPrompt(cfg, cwd, db, opts.ResumeSessionID)

	// Register memory tools (needs DB, not prompt logic).
	if db != nil {
		toolReg.Register(&tools.MemorySearchTool{DB: db})
		toolReg.Register(&tools.MemoryAddTool{DB: db})
		toolReg.Register(&tools.MemoryDeleteTool{DB: db})
		toolReg.Register(&tools.MemoryUpdateTool{DB: db})
		toolReg.Register(&tools.MemorySessionSearchTool{DB: db})
	}

	// --- MCP servers ------------------------------------------------------
	// Isolated mode omits MCP tools by policy: they execute on the host
	// through stdio/HTTP clients no Workspace can intercept, so
	// registering them would open host mutation paths around the
	// apply-out boundary. The servers still start (the session owns
	// their lifecycle); their tool names are recorded so the prompt and
	// the D4 session row can name what was left out. mcpToolNames is
	// then cleared for the loop config — the approval gate would prompt
	// for calls to tools that cannot dispatch.
	mcpClients, mcpInfos, mcpToolNames = initMCP(cfg, toolReg, skipMCP, sessionSandbox == nil)
	var omittedMCP []string
	if sessionSandbox != nil {
		for name := range mcpToolNames {
			omittedMCP = append(omittedMCP, name)
		}
		slices.Sort(omittedMCP)
		if len(omittedMCP) > 0 {
			fmt.Fprintf(os.Stderr, "warning: isolated mode omits %d MCP tool(s) — they run on the host and the workspace cannot contain them: %s\n",
				len(omittedMCP), strings.Join(omittedMCP, ", "))
		}
		mcpToolNames = nil
	}

	skillDirs := skillSearchPaths()
	skillTool := &tools.SkillTool{Dirs: skillDirs, ReadOnly: sessionSandbox != nil}
	if db != nil {
		skillTool.Embedder = db.Embedder()
	}
	toolReg.Register(skillTool)

	// skill and plan stay available in isolated mode but read-only:
	// their mutating actions write host harness configuration (.agents/,
	// tracked in git), which must not be written around apply_out. The
	// prompt delta says so; the tools refuse the mutating actions
	// themselves.
	planDirs := planSearchPaths()
	toolReg.Register(&tools.PlanTool{Dirs: planDirs, ReadOnly: sessionSandbox != nil})

	roleDirs := runner.RoleSearchPaths(cwd)
	// role is host-only (plan §3, decision D3): role files are harness
	// configuration the host reads. The registry above still loads the
	// host's roles; only the management tool is left out of the isolated
	// session.
	if sessionSandbox == nil {
		toolReg.Register(&tools.RoleTool{
			Dirs:         roleDirs,
			BuiltinFiles: runner.BuiltinRoleFiles(),
			Resolver:     runner.SubAgentRoleResolver{},
		})
	} else {
		omittedTools = append(omittedTools, "role")
	}

	var todoStore *todo.Store
	if db != nil {
		todoStore = todo.NewStoreWithDB(db)
		if err := todoStore.LoadFromDB(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not load todos: %v\n", err)
		}
	} else {
		todoStore = todo.NewStore()
	}
	toolReg.Register(&tools.TodoWriteTool{
		Store: todoStore,
		OnWrite: func() {
			if formatted := todoStore.Format(); formatted != "" {
				fmt.Fprintf(os.Stderr, "\n%s\n", formatted)
			}
		},
	})

	procMgr := processpkg.NewManager()
	// background_process is host-only: it drives this in-memory host
	// process manager. The manager itself stays — session teardown still
	// owns it — but the tool is not exposed in isolated mode.
	if sessionSandbox == nil {
		toolReg.Register(&tools.BackgroundProcessTool{Manager: procMgr})
	} else {
		omittedTools = append(omittedTools, "background_process")
	}

	var messages []types.Message
	var sessionID string
	var msgIdx int

	messages, sessionID, msgIdx, systemPrompt, err = restoreSession(db, opts.ResumeSessionID, systemPrompt)
	if err != nil {
		return nil, err
	}
	if opts.ResumeSessionID == "" {
		sessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
		if db != nil {
			cwd, _ := os.Getwd()
			db.CreateSession(memory.Session{
				ID:           sessionID,
				StartedAt:    time.Now().Unix(),
				CWD:          cwd,
				Model:        modelName,
				SystemPrompt: systemPrompt,
			})
		}
	}

	// Derive the top-level agent prompt (directives + quick-ref) from the
	// clean system prompt. systemPrompt stays clean so child sub-agent
	// prompts never inherit top-level directives.
	mainPrompt := buildMainPrompt(cfg, opts, systemPrompt, toolReg)

	tracker := &tools.ConflictTracker{}
	subAgentProvider, subAgentModel := resolveSubAgent(cfg)
	subCW := providers.ResolveWindow(modelName, cfg.Agent.Default.ContextWindow) / 2
	if subCW < 32000 {
		subCW = 32000
	}

	followupCh := make(chan string, 32)

	// Background sub-agents are managed session-wide: they derive
	// cancellation from a session root (not the per-call context), so
	// they survive the dispatching tool call and turn. Results are
	// delivered as follow-ups; usage is attributed to totalUsage.
	backgroundJobs := jobs.NewBackgroundJobs()
	backgroundJobs.MaxConcurrent = cfg.Agent.SubAgent.MaxConcurrency
	if backgroundJobs.MaxConcurrent <= 0 {
		backgroundJobs.MaxConcurrent = 4
	}

	// Shepherd infrastructure: session-shared trace store, effect bus,
	// and scope manager. Initialized before the task tool is built — the
	// session's root scope (shepherd-adoption plan D1) must exist when
	// the runner closure is constructed, since every sub-agent scope
	// forks from it and the supervisor's tree should mirror the dispatch
	// tree. The orchestrator pipeline no longer hosts shepherd_trace, so
	// this is the single initialization point for the supervisor tools,
	// the supervised task tool, and sub-agent trace middleware.
	var scopeMgr *shepherd.ScopeManager
	var rootScopeID string
	var runRegistry *supervision.Registry
	// The session's steering channel: the REPL's steer source and, from
	// A2 on, the supervisor's injection target for the root scope. Sized
	// with the registry's exported buffer so every channel the registry
	// borrows has the same capacity.
	steerCh := make(chan string, supervision.SteerBuffer)
	if cfg.Agent.Default.ShepherdTraceDir != "" {
		store, bus, mgr, serr := pipeline.InitShepherdInfrastructure(cfg.Agent.Default.ShepherdTraceDir, 0)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "warning: shepherd tracing disabled: %v\n", serr)
		} else {
			tools.SharedTraceStore = store
			scopeMgr = mgr
			tools.SharedScopeManager = mgr

			// Supervision bridge (adoption plan A2): scope IDs mapped
			// to running loops' steering channels. Session-owned and
			// injected into the task runner, the supervisor tool, and
			// the session teardown — never a process global. The
			// runner registers each sub-agent dispatch; the root
			// registers against the session's own steering channel,
			// so an inject into the root steers the session itself.
			runRegistry = supervision.NewRegistry()

			// Root scope (adoption plan D1): local mode shares the
			// in-place git sandbox — never destroyed, the kernel's
			// materializer contract; an isolated session keeps the
			// root pure-causal (the container is the substrate;
			// host-git capture would lie about the tree, isolation
			// plan D2). A failure leaves sub-agent scopes on the
			// legacy flat path rather than bricking tracing.
			if root, rerr := runner.CreateRootScope(mgr, sessionID, cfg.Agent.Default.SupervisedRepoPath, sessionSandbox != nil); rerr != nil {
				fmt.Fprintf(os.Stderr, "warning: no shepherd root scope — sub-agent scopes fall back to flat creation: %v\n", rerr)
			} else {
				rootScopeID = root.ID()
			}

			// Advisory rules engine (adoption plan B1): the kernel
			// supervisor watches the effect bus the trace store
			// publishes to, and a service drains its interventions
			// into the A2 bridge above — a rule firing becomes a
			// delivered steering message, recorded in the trace.
			// Constructed after the root scope so rule firings
			// against it can be dropped: the parent's own calls
			// already passed the approval middleware, and the exit
			// criterion is explicit — the parent session stays
			// untouched. The bus is otherwise inert (its first
			// consumer, per the adoption audit); disabled by config
			// only, since tracing is already the opt-in.
			supervisorCfg := cfg.Agent.Default.Supervisor
			rules := supervision.AdvisoryRules{
				DestructiveTool:      supervisorCfg.DestructiveRuleEnabled(),
				HighErrorRate:        supervisorCfg.HighErrorRateEnabled(),
				ErrorRateThreshold:   supervisorCfg.ErrorRateThreshold(),
				ErrorRateWindow:      supervisorCfg.ErrorRateWindow(),
				StuckDetection:       supervisorCfg.StuckDetectionEnabled(),
				StuckRepeatThreshold: supervisorCfg.StuckRepeatThreshold(),
			}
			// The blocking shell guard (adoption plan B2) rides the
			// same service: the engine gate covers it, and its check
			// is the synchronous pre-dispatch seam the sub-agent
			// pipeline consults.
			guardEnabled := supervisorCfg.DestructiveGuardEnabled()
			if supervisorCfg.EngineEnabled() && (rules.Any() || guardEnabled) {
				supervisorSvc = supervision.NewService(mgr, bus, runRegistry, rules, guardEnabled, rootScopeID)
				supervisorSvc.Start("yaah-supervisor-" + sessionID)
			}
		}
	}

	// Role routing: each role dispatches through exactly one sub-agent
	// tool, selected by the per-role `supervised` flag
	// (subagent.roles.<name>.supervised). Start with every role on
	// spawn_subagent; supervised roles are moved to supervised_task only
	// after shepherd infrastructure initializes successfully, so a role is
	// never unreachable when tracing is unset or init fails.
	isSupervisedRole := func(name string) bool {
		return cfg.Agent.SubAgent.Roles[name].Supervised
	}

	// Isolated session (plan §1.3, decision D2): sub-agents inherit the
	// session's one sandbox, so the TaskTool gets the same sandbox
	// workspace the main registry runs on — a sub-agent's file tools and
	// shell dispatch inside the container, never on the host. Nil in
	// local mode, where the path validator route below applies instead.
	// Sub-agents also get no host spill dir: a truncation hint pointing
	// at a host file is unreadable through their sandboxed read tool,
	// so the loop falls back to the bare truncation marker instead.
	var subAgentWorkspace tools.Workspace
	var subAgentSpillDir string
	if sessionSandbox == nil {
		subAgentSpillDir = toolSpillDir()
	} else {
		subAgentWorkspace = sessionSandbox.Workspace()
	}

	taskTool := runner.NewTaskTool(runner.TaskToolOpts{
		Provider:              provider,
		SystemPrompt:          buildSubAgentBasePrompt(cwd, db),
		ModelName:             modelName,
		DB:                    db,
		SessionID:             sessionID,
		SubAgentProvider:      subAgentProvider,
		SubAgentModel:         subAgentModel,
		SubCfg:                cfg.Agent.SubAgent,
		RoleNames:             reg.Names(),
		OtelEnabled:           cfg.Observability.Otel.Enabled,
		OtelVerbose:           cfg.Observability.Otel.Verbose,
		Tracker:               tracker,
		EstimateFactor:        cfg.Agent.Default.EstimateFactor,
		SubContextWindow:      subCW,
		OutputLimit:           cfg.Agent.SubAgent.OutputLimit,
		ProviderMap:           cfg.Providers,
		Defaults:              cfg.Agent.Default,
		ToolSpillDir:          subAgentSpillDir,
		PathValidator:         pathValidator,
		Workspace:             subAgentWorkspace,
		RootScopeID:           rootScopeID,
		Runs:                  runRegistry,
		GuardCheck:            supervisorSvcGuardCheck(supervisorSvc),
		ResolveProviderByName: resolveProviderByName,
	})

	// RoleResolver provides a live role-name lookup so the spawn_subagent
	// tool sees roles created via the role tool without a restart. Until
	// supervised_task is registered it returns every role; it is narrowed
	// to the non-supervised roles after successful shepherd init below.
	taskTool.RoleResolver = func() []string { return subagent.DefaultRegistry().Names() }

	// Hand the manager to the task tool so background:true dispatches go
	// through it instead of the broken inline goroutine.
	taskTool.BackgroundJobs = backgroundJobs

	// Deliver completed background results as follow-up messages. This
	// is a BLOCKING send (guarded by the manager's Done channel) so
	// results are never dropped: a job whose result cannot be queued yet
	// blocks in its own goroutine until the next turn drains the channel
	// or the session closes.
	bgDone := backgroundJobs.Done()
	backgroundJobs.Deliver = func(role, description, result string, err error) {
		prefix := "[BACKGROUND"
		if role != "" {
			prefix += " " + role
		}
		prefix += "]"
		if description != "" {
			prefix += " " + description
		}
		text := result
		if err != nil {
			text = "error: " + err.Error()
		}
		msg := prefix + " completed:\n" + text
		select {
		case followupCh <- msg:
		case <-bgDone:
		}
	}

	toolReg.Register(taskTool)

	toolReg.Register(&tools.SubAgentJobsTool{Jobs: backgroundJobs})

	// Shepherd-dependent tool surface: the supervisor tool and the
	// supervised task tool register only when the infrastructure above
	// came up, and the supervised/plain role split lands with them.
	if scopeMgr != nil {
		// Supervised roles move from spawn_subagent to supervised_task
		// now that the shared scope manager is actually available.
		// Isolated mode folds them back: supervised_task is omitted
		// there, and leaving the roles split off spawn_subagent would
		// leave them with no dispatch tool at all (task rejects them
		// by name).
		plainRoles, supervisedRoles := splitRolesBySupervised(reg.Names(), isSupervisedRole)
		if sessionSandbox != nil {
			plainRoles = reg.Names()
			supervisedRoles = nil
		}
		taskTool.RoleNames = plainRoles
		taskTool.RoleResolver = func() []string {
			names := subagent.DefaultRegistry().Names()
			if sessionSandbox != nil {
				return names
			}
			plain, _ := splitRolesBySupervised(names, isSupervisedRole)
			return plain
		}
		taskTool.RoleDescriptions = runner.RoleDescriptionsFor(plainRoles)

		// Supervisor tool for sub-agent supervision (list_scopes/inject/halt/status).
		toolReg.Register(&tools.SupervisorTool{
			TraceDir:    cfg.Agent.Default.ShepherdTraceDir,
			RootScopeID: rootScopeID,
			Runs:        runRegistry,
			// supervisor.halt_root (adoption plan A3): root halt maps to
			// cancelling the session's current run — a big hammer that
			// needs the explicit opt-in, off by default. HaltRootEnabled
			// resolves the stanza over the legacy flat key.
			HaltRoot: cfg.Agent.Default.HaltRootEnabled(),
		})

		// Supervised task tool — blocking sub-agent execution with
		// checkpoint/rollback/retry. Shares the runner and timeout
		// logic with the task tool, but runs synchronously and wraps
		// each attempt in a git checkpoint. Only registered when
		// tracing is on: checkpoint/rollback needs the shared scope
		// manager. It offers only roles flagged `supervised`; plain
		// roles stay with spawn_subagent.
		maxRetries := cfg.Agent.Default.SupervisedMaxRetries
		if maxRetries <= 0 {
			maxRetries = 1
		}
		// supervised_task is host-only: it provisions sandboxes, so
		// it cannot run inside the isolation it creates. Omitted in
		// isolated mode (plan §3); its roles stay assigned to it,
		// which the prompt delta explains.
		if sessionSandbox == nil {
			toolReg.Register(&tools.SupervisedTaskTool{
				Runner:         taskTool.Runner,
				ResolveTimeout: taskTool.ResolveTimeout,
				RoleNames:      supervisedRoles,
				RoleResolver: func() []string {
					_, supervised := splitRolesBySupervised(subagent.DefaultRegistry().Names(), isSupervisedRole)
					return supervised
				},
				RoleDescriptions: runner.RoleDescriptionsFor(supervisedRoles),
				RepoPath:         cfg.Agent.Default.SupervisedRepoPath,
				MaxRetries:       maxRetries,

				Worktree:          cfg.Agent.Default.SupervisedWorktree,
				WorktreeRoot:      cfg.Agent.Default.SupervisedWorktreeRoot,
				WorktreeBootstrap: cfg.Agent.Default.SupervisedWorktreeBootstrap,
			})
		} else {
			omittedTools = append(omittedTools, "supervised_task")
		}
	}

	// The prompt must never advertise a tool that is not registered.
	// Local mode: the sub-agent guidance only when supervised_task is
	// actually there (the shared scope manager is set exclusively on
	// successful init). Isolated mode: the §2.2 delta instead — which
	// names every omitted tool with its reason, covers the
	// spawn_subagent-only story, and explains the apply_out workflow —
	// since the local guidance would promise a supervised_task that
	// cannot exist here.
	if sessionSandbox != nil {
		slices.Sort(omittedTools)
		mainPrompt += "\n\n" + prompts.IsolatedWorkspaceGuidance(omittedTools, omittedMCP)
	} else if tools.SharedScopeManager != nil {
		mainPrompt += "\n\n" + prompts.SubAgentToolsGuidance()
	}

	toolReg.Register(&tools.ListSubAgentsTool{
		Lister: func() []tools.SubAgentInfo {
			r := subagent.DefaultRegistry()
			if r == nil {
				return nil
			}
			defs := r.List()
			infos := make([]tools.SubAgentInfo, 0, len(defs))
			for name, def := range defs {
				desc := def.Description
				if desc == "" {
					desc = def.Body
					if idx := strings.IndexByte(desc, '\n'); idx >= 0 {
						desc = desc[:idx]
					}
				}

				// Effective budget for a dispatch without per-call
				// overrides — resolved exactly like makeTaskRunner
				// resolves it, so the orchestrator sees real budgets
				// instead of guessing (subagent-turn-budget-floors §4.7).
				rc := cfg.Agent.SubAgent.Roles[string(name)]
				res := budget.Resolve(budget.Spec{
					RoleMaxIterations: def.MaxLoopCycles,
					RoleMinIterations: def.MinLoopCycles,
					RoleMaxTurns:      def.MaxToolTurns,
					RoleMinTurns:      def.MinToolTurns,
					CfgMaxIterations:  rc.MaxLoopCycles,
					CfgMinIterations:  rc.MinLoopCycles,
					CfgMaxTurns:       rc.MaxToolTurns,
					CfgMinTurns:       rc.MinToolTurns,
					DefaultTurns:      cfg.Agent.SubAgent.DefaultMaxToolTurns,
					DefaultMinTurns:   cfg.Agent.SubAgent.DefaultMinToolTurns,
					HardCeiling:       budget.SchemaMaxIterations,
				})

				infos = append(infos, tools.SubAgentInfo{
					Role:        string(name),
					DisplayName: def.DisplayName,
					Specialty:   def.Specialty,
					Contract: tools.SubAgentContract{
						Heading: def.Contract.Heading,
						Fields:  runner.ConvertContractFields(def.Contract.Fields),
					},
					Description: desc,
					Tools:       def.Tools,

					Iterations:    res.Iterations,
					Turns:         res.Turns,
					MinIterations: budget.FirstPositive(rc.MinLoopCycles, def.MinLoopCycles),
					MinTurns:      budget.FirstPositive(rc.MinToolTurns, def.MinToolTurns, cfg.Agent.SubAgent.DefaultMinToolTurns),
				})
			}
			return infos
		},
	})

	// Isolated mode: hand the registry the sandbox workspace only after
	// every tool is registered. SetWorkspace is the enforcement point — a
	// refusal fails the session loudly rather than half-isolating it, so
	// nothing below can believe a contained run is in effect when it is
	// not. The destroy joins the error: a teardown failure means the
	// container may still be running.
	if sessionSandbox != nil {
		if serr := toolReg.SetWorkspace(sessionSandbox.Workspace()); serr != nil {
			// Teardown failure joins the returned error — a destroy that
			// failed means the container may still be running. The guard
			// is then stood down: reporting one failure on two channels
			// (and destroying twice through the once-guard) helps no one.
			serr = fmt.Errorf("isolated workspace: %w", serr)
			destroyCtx, cancelDestroy := context.WithTimeout(context.Background(), workspaceSandboxDestroyTimeout)
			defer cancelDestroy()
			if derr := sessionSandbox.Destroy(destroyCtx); derr != nil {
				serr = errors.Join(serr, fmt.Errorf("workspace sandbox teardown: %w", derr))
			}
			sandboxTeardownHandled = true
			return nil, serr
		}
	}

	// Decision D4: the workspace substrate and the omitted tool names go
	// into the session record, so "the model didn't use
	// background_process" and "the model couldn't" stay distinguishable
	// for any reader of the transcript. Written only after the
	// SetWorkspace gate above has passed — a run the gate refused never
	// started, and must not overwrite the record of the last one that
	// did. A resume that changed mode overwrites, describing the most
	// recent actual run.
	if db != nil {
		backend := cfg.Workspace.Backend
		if backend == "" {
			backend = config.BackendLocal
		}
		recorded := append(slices.Clone(omittedTools), omittedMCP...)
		slices.Sort(recorded)
		if werr := db.UpdateSessionWorkspace(sessionID, backend, strings.Join(recorded, ",")); werr != nil {
			fmt.Fprintf(os.Stderr, "warning: recording workspace metadata: %v\n", werr)
		}
	}

	// The last fallible step has passed: the startup ownership guard can
	// no longer fire, so the embedding backfill can launch without racing
	// the guard's db.Close. Backfill embeddings for any memory entries
	// created before the embedding server was configured.
	if db != nil && embedderAttached {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			n, err := db.ReconcileEmbeddings(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: embedding backfill: %v\n", err)
			} else if n > 0 {
				fmt.Fprintf(os.Stderr, "embedded %d existing memory entries\n", n)
			}
		}()
	}

	// Wrap the provider with OTel instrumentation if enabled.
	provider = wrapProviderWithOtel(provider, otelActive, cfg.Observability.Otel.Verbose)

	sess := &agentSession{
		cfg:            cfg,
		opts:           opts,
		provider:       provider,
		providerName:   providerName,
		modelName:      modelName,
		systemPrompt:   systemPrompt,
		mainPrompt:     mainPrompt,
		toolReg:        toolReg,
		db:             db,
		mcpClients:     mcpClients,
		mcpInfos:       mcpInfos,
		mcpToolNames:   mcpToolNames,
		procMgr:        procMgr,
		sessionID:      sessionID,
		messages:       messages,
		msgIdx:         msgIdx,
		otelShutdown:   otelShutdown,
		tracker:        tracker,
		backgroundJobs: backgroundJobs,
		sessionSandbox: sessionSandbox,
		cwd:            cwd,
		steerCh:        steerCh,
		rootScopeID:    rootScopeID,
		runs:           runRegistry,
		supervisor:     supervisorSvc,
		followupCh:     followupCh,
	}
	// Root-scope bridge registration (adoption plan A2/A3): inject into
	// the root steers the session (its own steering channel); halting it
	// cancels the session's current run — the supervisor.halt_root
	// opt-in the SupervisorTool carries. Registered after the session
	// literal because the cancel is the session's method.
	if rootScopeID != "" && runRegistry != nil {
		// The root registers no quiesce signal: its sandbox (in-place
		// git, or none at all in isolated mode) is never destroyed by a
		// halt, so there is nothing a still-running prompt could race.
		runRegistry.Register(rootScopeID, supervision.Run{
			SteerCh: steerCh,
			Cancel:  sess.HaltCurrentRun,
		})
	}

	// The session owns everything created above from here: close() will
	// destroy the sandbox, and neither startup guard must touch the
	// session's resources again.
	sandboxTeardownHandled = true
	sessionHandedOff = true

	// Ask fallback for out-of-workspace access: prompts route through
	// the session's approval UI (TUI/web) or stdin in plain REPL mode.
	if pathValidator != nil && (opts.WorkspaceAsk || cfg.Agent.Default.WorkspaceAsk) {
		pathValidator.AskFn = sess.promptWorkspaceAccess
	}

	// Attribute completed background sub-agent usage to the session
	// total (session-scoped, so never lost across turns/runs).
	backgroundJobs.OnUsage = func(u types.Usage) {
		sess.mu.Lock()
		sess.totalUsage.PromptTokens += u.PromptTokens
		sess.totalUsage.CompletionTokens += u.CompletionTokens
		sess.totalUsage.TotalTokens += u.TotalTokens
		sess.mu.Unlock()
	}

	return sess, nil
}

// supervisorSvcGuardCheck extracts the blocking pre-dispatch check
// (adoption plan B2) from the session's supervisor service. A nil
// service — tracing off, the engine disabled, or a startup that never
// reached the shepherd branch — means no guard: every sub-agent call
// dispatches, byte-identical to a tracing-off session.
func supervisorSvcGuardCheck(svc *supervision.Service) func(ownerID, toolName, argsJSON string) string {
	if svc == nil {
		return nil
	}
	return svc.GuardCheck()
}

// splitRolesBySupervised partitions role names into plain and supervised
// sets using the supplied predicate (the per-role `supervised` flag). A
// role dispatches through exactly one sub-agent tool: plain roles via
// spawn_subagent, supervised roles via supervised_task. Order within each
// set preserves the input order.
func splitRolesBySupervised(names []string, isSupervised func(string) bool) (plain, supervised []string) {
	for _, n := range names {
		if isSupervised(n) {
			supervised = append(supervised, n)
		} else {
			plain = append(plain, n)
		}
	}
	return plain, supervised
}
