package yaah

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"github.com/buchenberg/goatherd-runtime/sandbox/egress"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/recovery"
	"github.com/buchenberg/yaah/internal/agent/runner"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	processpkg "github.com/buchenberg/yaah/internal/process"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/buchenberg/yaah/internal/worldinfo"
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
// via DelegateToolOpts.ToolSpillDir — internal/agent never reads config
// paths directly (finding C4).
func toolSpillDir() string {
	return filepath.Join(config.HomeDir(), "truncated")
}

// rolefileDirsMu guards the accumulated rolefile search directories.
// rebuildDefaultRoleRegistry appends a session's dirs and rebuilds the
// process-wide role registry as their union, so a later session build
// atomically swaps in a registry that still carries every dir earlier
// sessions contributed (multi-session ACP; a plain
// SetDefaultRoleRegistry per build would silently replace the old one).
var (
	rolefileDirsMu sync.Mutex
	rolefileDirs   []string
)

// rebuildDefaultRoleRegistry accumulates the session's rolefile search
// directories and rebuilds the process-wide role registry as built-ins
// plus the union of every directory seen so far, then swaps that in as
// the default.
func rebuildDefaultRoleRegistry(cwd string) {
	rolefileDirsMu.Lock()
	for _, dir := range runner.RoleSearchPaths(cwd) {
		dup := false
		for _, seen := range rolefileDirs {
			if seen == dir {
				dup = true
				break
			}
		}
		if !dup {
			rolefileDirs = append(rolefileDirs, dir)
		}
	}
	dirs := make([]string, len(rolefileDirs))
	copy(dirs, rolefileDirs)
	rolefileDirsMu.Unlock()

	// Same tolerance as the original per-build loader and the hot-reload
	// path: unreadable/on-missing dirs are skipped.
	reg := subagent.NewRoleRegistry()
	if files := runner.BuiltinRoleFiles(); files != nil {
		_ = reg.LoadBytes(files)
	}
	for _, dir := range dirs {
		// LoadDir itself tolerates missing dirs; other read errors are
		// skipped, as in the original per-build loader.
		_ = reg.LoadDir(dir)
	}
	subagent.SetDefaultRoleRegistry(reg)
}

// canonicalStoredCwd normalizes a session's working directory before it
// is persisted: the same canonicalization (absolute, symlink-resolved)
// the engine-level session registry and its filters apply, so stored
// rows and their filters always agree. Best-effort — an unresolvable
// path is stored as given rather than failing session creation.
func canonicalStoredCwd(cwd string) string {
	return cwdpath.CanonicalStored(cwd)
}

// newAgentSessionWithOptions creates an agent session. All CLI-flag and
// serve-mode inputs arrive via opts — no hidden package state. When
// skipMCP is true, MCP server subprocesses are not started (they consume
// CPU and make tview input sluggish on Windows). When skipOtel is true,
// OTel exporters are not initialised.
func newAgentSessionWithOptions(opts SessionOptions, skipMCP, skipOtel bool) (*agentSession, error) {
	// The bare --resume sentinel resolves only on interactive surfaces
	// (REPL, TUI, one-shot) which run maybeResolveResumePicker first.
	// Non-interactive readers (serve, web, ACP) must name a session.
	if opts.ResumeSessionID == resumePickerSentinel {
		return nil, fmt.Errorf("--resume without a value opens the interactive picker, which is not available in this mode; pass --resume=<session-id>")
	}
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
		// egressBroker is the session's egress broker (Plan 7 Phase 3), started
		// only when agents.subagent.network is "broker". Declared here, like the
		// supervisor, so the guard below stops a partially-built session's broker
		// — it owns a listener and a goroutine.
		egressBroker *egress.Broker
	)
	sessionHandedOff := false
	defer func() {
		if sessionHandedOff {
			return
		}
		if supervisorSvc != nil {
			supervisorSvc.Close()
		}
		if egressBroker != nil {
			egressBroker.Stop()
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
	//
	// A caller-supplied cwd (ACP sessions) wins over the process cwd and
	// is never applied with os.Chdir: several sessions with different
	// workspaces must be able to share one process.
	cwd := opts.Cwd
	if cwd == "" {
		var gerr error
		cwd, gerr = os.Getwd()
		if gerr != nil {
			return nil, fmt.Errorf("determine the working directory: %w", gerr)
		}
	}

	// Role registry: built-in (embedded) definitions plus user-defined
	// rolefiles from the searched directories (project dirs walked up
	// from cwd, then ~/.agents/roles). Multi-session (ACP): a session
	// with a different cwd must not *overwrite* the registry earlier
	// sessions still dispatch against, so one union registry is rebuilt
	// and atomically swapped per build over every dir seen so far.
	// Fully per-session registries remain future work — dispatch
	// consults the process-wide DefaultRegistry — so the union trades
	// same-name rolefile isolation for keeping earlier sessions usable.
	rebuildDefaultRoleRegistry(cwd)

	// The tool registry (plan §3, Option A): local mode installs every
	// built-in; isolated mode curates the set — the host-only leaf tools
	// are left out here, the runtime-wired host-only tools at their
	// registration sites below — so Registry.SetWorkspace's gate finds
	// both diagnostic lists empty and the isolated session can actually
	// be selected. The model is told about the delta (plan §2.2) and the
	// record keeps it (decision D4).
	isolated := cfg.Workspace.Backend == config.BackendContainerd
	// Tool belt (yaah-w8x): the session's allowlist of tools. The belt
	// is enforced by the registry at registration — a tool it does not
	// admit is never installed, so a late registration is refused like
	// any other. An unset list admits every tool (the default).
	belt := tools.NewBelt(cfg.Agent.Default.Tools)
	var omittedTools []string
	var toolReg *tools.Registry
	if isolated {
		toolReg = tools.NewIsolatedRegistryWithBelt(belt)
		// Derived from the same hostOnly() marker the registry filter
		// and the SetWorkspace gate read, so the prompt delta and the
		// D4 record can never name a tool the session actually has, or
		// miss one it does not.
		omittedTools = append(omittedTools, tools.HostOnlyLeafTools()...)
	} else {
		toolReg = tools.NewRegistryWithBelt(belt)
	}

	// One-shot sessions are non-interactive: a blocking question has
	// no one to answer it, so the tool is left out of the belt and the
	// prompt guidance (below) says so.
	if opts.OneShot {
		toolReg.Unregister("question")
	}

	// Go toolchain tools in a workspace that is not a Go module: the
	// same rule the sub-agent path already applies (runner.go's
	// subAgentUnavailableTools, yaah-9n0). go_test/go_mod/go_outline/
	// go_refactor/staticcheck drive the Go toolchain against the
	// workspace, so outside a module they cannot succeed — the schema is
	// per-call cost that buys a failed call later.
	//
	// Resolved ONCE here, from the project root, and never re-checked:
	// the tools array is part of the cached prompt prefix, so a set that
	// flipped mid-session (the agent runs `go mod init`) would invalidate
	// the cache on every later call and cost far more than the schemas
	// save. In isolated mode the container holds a copy of this project,
	// so the host root is the right proxy — the same substitution
	// subAgentUnavailableTools documents.
	if !tools.GoToolAvailable(cwd) {
		for _, name := range tools.GoToolchainLeafTools() {
			toolReg.Unregister(name)
			// The tools exist and this session merely cannot use them, so
			// an allowlist naming one is not a typo.
			belt.Know(name)
			omittedTools = append(omittedTools, name)
		}
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
	default:
		// Every local session gets a validator so relative paths and
		// shell commands resolve against the session's own cwd rather
		// than the process cwd (ACP serves several sessions, each
		// with its own workspace, in one process). With no
		// --workspace there is no containment: keep the legacy
		// unrestricted behaviour, including ~ expansion and no deny
		// patterns.
		allowHome := opts.AllowHomeAccess
		var patterns []string
		if opts.WorkspaceRoot != "" {
			patterns = deny
		} else {
			allowHome = true
		}
		pathValidator = tools.NewPathValidator(opts.WorkspaceRoot, allowHome, patterns)
		pathValidator.WorkDir = cwd
		toolReg.SetPathValidator(pathValidator)
	}

	// The accept seam (plan §2.3, D1): the single door changes leave the
	// container through, registered only in isolated mode — in local mode
	// the tools write the host directly and there is nothing to let out.
	//
	// The pointer is held because the effect sink cannot be attached here: the
	// trace store, effect bus and root scope are all created further down. The
	// registry holds the same pointer, so setting the field later reaches the
	// registered tool — and doing it during wiring, before any turn can
	// dispatch, means no call can observe the tool half-configured.
	var applyOutTool *tools.ApplyOutTool
	if sessionSandbox != nil {
		applyOutTool = &tools.ApplyOutTool{Sandbox: sessionSandbox, HostDir: cwd}
		toolReg.Register(applyOutTool)
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
	// Memory tools are wired only when there is a store to put them in AND
	// the session can benefit from one. A one-shot run is a single
	// non-interactive turn: there is no later conversation to recall into,
	// and the store a CI or bench one-shot writes is typically discarded
	// with its container — so the schemas are per-call cost, and an agent
	// that "saves a finding" spends output tokens on a note nobody will
	// read. Same precedent as `question` above.
	//
	// The Memory Guidelines block in the system prompt is gated on this
	// SAME value: the prompt must never advertise a tool the session did
	// not register.
	memoryTools := db != nil && !opts.OneShot
	systemPrompt := buildSystemPrompt(cfg, cwd, db, opts.ResumeSessionID, memoryTools)

	// Register memory tools (needs DB, not prompt logic).
	if memoryTools {
		toolReg.Register(&tools.MemorySearchTool{DB: db})
		toolReg.Register(&tools.MemoryAddTool{DB: db})
		toolReg.Register(&tools.MemoryPromoteTool{DB: db})
		toolReg.Register(&tools.MemoryDeleteTool{DB: db})
		toolReg.Register(&tools.MemoryUpdateTool{DB: db})
		toolReg.Register(&tools.MemorySessionSearchTool{DB: db})
	} else {
		// The tools exist but this session has nowhere to put them (no
		// database) or no use for them (one-shot): the belt must know the
		// names, or an allowlist that names them reads as a typo.
		belt.Know("memory_search", "memory_add", "memory_delete", "memory_update", "memory_promote", "memory_search_sessions")
		for _, name := range []string{"memory_search", "memory_add", "memory_delete", "memory_update", "memory_promote", "memory_search_sessions"} {
			omittedTools = append(omittedTools, name)
		}
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
	mcpClients, mcpInfos, mcpToolNames = initMCP(cfg, toolReg, skipMCP, sessionSandbox == nil, opts.MCPManifests, cwd)
	var omittedMCP []string
	if sessionSandbox != nil {
		for name := range mcpToolNames {
			omittedMCP = append(omittedMCP, name)
			// The MCP tools exist in this session's configuration even
			// though the workspace leaves them out: the belt must know
			// the names, or an allowlist that names them reads as a typo.
			belt.Know(name)
		}
		slices.Sort(omittedMCP)
		if len(omittedMCP) > 0 {
			fmt.Fprintf(os.Stderr, "warning: isolated mode omits %d MCP tool(s) — they run on the host and the workspace cannot contain them: %s\n",
				len(omittedMCP), strings.Join(omittedMCP, ", "))
		}
		mcpToolNames = nil
	}

	skillDirs := skillSearchPaths()
	// Strict (the autonomous-write security scan) is opt-in and defaults off:
	// it is meant to guard unattended consolidation writes, not human-directed
	// skill editing. No caller enables it yet — the unattended write path lands
	// in P3/P4 (docs/compaction-as-sleep.md §8). When that lands, derive it
	// from an explicit "unattended" session mode, not the sandbox flag.
	skillTool := &tools.SkillTool{Dirs: skillDirs, ReadOnly: sessionSandbox != nil}
	if db != nil {
		skillTool.Embedder = db.Embedder()
		// Skill lifecycle tracking (usage, provenance, mutation ledger).
		// Left nil when the session has no store: the tool then behaves
		// exactly as it did before P1.
		skillTool.Store = db
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
		belt.Know("role")
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
		belt.Know("background_process")
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
			db.CreateSession(memory.Session{
				ID:           sessionID,
				StartedAt:    time.Now().Unix(),
				CWD:          canonicalStoredCwd(cwd),
				Model:        modelName,
				SystemPrompt: systemPrompt,
			})
		}
	}

	// Derive the top-level agent prompt (with directives) from the clean
	// system prompt. systemPrompt stays clean so child sub-agent prompts
	// never inherit top-level directives.
	mainPrompt := buildMainPrompt(cfg, opts, systemPrompt)

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
	// tree. The orchestrator and sub-agent pipelines share this store, so
	// this is the single initialization point for the supervisor tools
	// and both trace middlewares.
	var scopeMgr *goatherd.ScopeManager
	var traceStore *shepherdtrace.SQLiteTraceStore
	var traceBus *shepherdtrace.EffectBus
	var rootScopeID string
	// retainCapable reports whether the root scope can actually capture
	// workspace state. CreateRootScope demotes an uncapturable directory
	// (e.g. not a git repo) to pure-causal — it still returns a root with
	// an ID, but with a nil sandbox, and a retained dispatch would then
	// fail at CaptureWorkspace. The retained lane is only live when both
	// the root exists and it has a sandbox.
	retainCapable := false
	var runRegistry *supervision.Registry
	// The session's steering channel: the REPL's steer source and, from
	// A2 on, the supervisor's injection target for the root scope. Sized
	// with the registry's exported buffer so every channel the registry
	// borrows has the same capacity.
	steerCh := make(chan string, supervision.SteerBuffer)
	// Tracing is integral: the session store always initializes. A
	// store-open failure is the runtime safety net, not a config switch —
	// warn and continue without a root scope.
	{
		store, bus, mgr, serr := pipeline.InitShepherdInfrastructure(cfg.Agent.Default.TracePath(), 0)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "warning: shepherd trace store unavailable, continuing without a root scope: %v\n", serr)
		}
		// Scope recovery (adoption plan R1, decision D5): the trace is
		// the registry — rebuild the scope tree, checkpoints, and
		// sealed outputs from the durable records so a restarted
		// process remembers its supervision state. The recovered
		// manager replaces the fresh one. A recovery failure fails
		// loudly (half-adopted sandboxes are worse than none) by
		// disabling the ENTIRE tracing block below — root scope,
		// bridge, and supervisor all stay down rather than
		// half-running against a closed store.
		tracingReady := serr == nil
		if tracingReady {
			repoPath := cfg.Agent.Default.RetainedRepoPath
			if repoPath == "" {
				repoPath, _ = os.Getwd()
			}
			// adoptRoots: an isolated session keeps roots causal (D2) —
			// recovery must not attach a host git sandbox where creation
			// refused one.
			// Plan 07 H5: the kernel's scan stats ride the recovery —
			// the scan is O(history), and the cost stays visible long
			// before it matters.
			var stats goatherd.RecoveryStats
			recovered, report, rerr := recovery.Recover(context.Background(), store, repoPath, sessionSandbox == nil, &stats)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "warning: shepherd tracing disabled: scope recovery failed: %v\n", rerr)
				store.Close(context.Background())
				tracingReady = false
			} else {
				mgr = recovered
				printRecoveryReport(report)
				printRecoveryStats(&stats)
			}
		}
		if tracingReady {
			scopeMgr = mgr
			// Session-owned copies for the clean-exit close (plan 07,
			// H2): the session's close() closes the infrastructure the
			// wiring created — see agentSession.traceStore. The manager
			// and store reach the tools and the runner by injection
			// (plan 07, H4), never through package globals.
			traceStore, traceBus = store, bus

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
			if root, rerr := runner.CreateRootScope(mgr, sessionID, cfg.Agent.Default.RetainedRepoPath, sessionSandbox != nil); rerr != nil {
				if errors.Is(rerr, runner.ErrRootTerminal) {
					// The explicit degraded mode (R2 review): the
					// resumed session's root already ended, so its
					// scope-tree supervision is over — sub-agent
					// scopes run flat and unsupervised for this
					// session. Name it; do not bury it in the
					// generic no-root warning.
					fmt.Fprintf(os.Stderr, "warning: %v — sub-agent scopes run unsupervised (flat) for this session\n", rerr)
				} else {
					fmt.Fprintf(os.Stderr, "warning: no shepherd root scope — sub-agent scopes fall back to flat creation: %v\n", rerr)
				}
			} else {
				rootScopeID = root.ID()
				retainCapable = root.Sandbox() != nil

				// Late-bind the accept seam's effect sink, now that the
				// scope it records against exists. The root scope is the
				// FactAppender (it carries AppendFact and OwnerID), so an
				// accept records its workspace effects on the session's own
				// owner path rather than on a synthetic one.
				//
				// Attached only when a root scope was created: without one
				// there is no owner path to append to, and the tool's nil
				// sink makes recording a silent no-op instead of an error
				// on every accept.
				if applyOutTool != nil {
					applyOutTool.SetEffects(&tools.EffectSink{
						Appender: root,
						Bus:      bus,
						OpID:     rootScopeID,
					})
				}
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
				// haltRoot is the B3 gate: a rule-driven halt of the session root
				// (the ctrl-C equivalent) dispatches only under the
				// supervisor.halt_root opt-in.
				supervisorSvc = supervision.NewService(mgr, bus, runRegistry, rules, guardEnabled, cfg.Agent.Default.HaltRootEnabled(), rootScopeID)
				supervisorSvc.Start("yaah-supervisor-" + sessionID)
			}
		}
	}

	// Isolated session (plan §1.3, decision D2): sub-agents inherit the
	// session's one sandbox, so the DelegateTool gets the same sandbox
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

	// Egress (Plan 7 Phase 3): agents.subagent.network selects the confined
	// dispatch's posture. "broker" needs a live broker, so one is started for the
	// session and its port pinned — the same object the guard above stops on a
	// failed startup and the session stops on close.
	subNetwork, broker, nerr := wireSubAgentNetwork(cfg.Agent.SubAgent)
	if nerr != nil {
		return nil, nerr
	}
	egressBroker = broker

	// Per-dispatch container worlds (yaah-ihfh.3, yaah-a7ks): a Linux host with
	// a container image can give a REPORT dispatch its own sibling container.
	// In a host session the sibling copies the host repository in; in a
	// container session it is seeded from the session container's captured
	// state. In a container session both report and proposal lanes use a
	// sibling; a host session keeps the host-settled GitDelta path.
	var dispatchWorlds *tools.DispatchWorlds
	if runtime.GOOS == "linux" && cfg.Workspace.Containerd.Image != "" {
		dispatchWorlds = &tools.DispatchWorlds{
			Config: cfg.Workspace,
			Opener: tools.OpenContainerdWorkspaceSandbox,
			Deny:   deny,
		}
		if sessionSandbox != nil {
			dispatchWorlds.SessionSandbox = sessionSandbox
		}
	}
	containerSession := dispatchWorlds != nil && dispatchWorlds.SessionSandbox != nil
	retainLane := retainedLaneActive(cfg.Agent.SubAgent.Mode, retainCapable, containerSession)

	// Session-level posture for every UI (TUI info pane, REPL banner, web
	// header, ACP _meta). One derivation shared with doctor, computed after
	// the containment switch so the sandbox flag is final: the surfaces must
	// describe the world a dispatch would actually get.
	worldStatus := worldinfo.Derive(cfg, opts.WorkspaceRoot, sessionSandbox != nil)

	taskTool := runner.NewDelegateTool(runner.DelegateToolOpts{
		Provider:              provider,
		ProviderName:          providerName,
		SystemPrompt:          buildSubAgentBasePrompt(cwd, db),
		ModelName:             modelName,
		DB:                    db,
		SessionID:             sessionID,
		SubAgentProvider:      subAgentProvider,
		SubAgentModel:         subAgentModel,
		SubCfg:                cfg.Agent.SubAgent,
		RoleNames:             subagent.Roles(),
		OtelEnabled:           cfg.Observability.Otel.Enabled,
		OtelVerbose:           cfg.Observability.Otel.Verbose,
		Tracker:               tracker,
		EstimateFactor:        cfg.Agent.Default.EstimateFactor,
		SubContextWindow:      subCW,
		OutputLimit:           cfg.Agent.SubAgent.OutputLimit,
		ProviderMap:           cfg.Providers,
		Defaults:              cfg.Agent.Default,
		PipelineDisabled:      cfg.Agent.Middleware.Disabled,
		ToolSpillDir:          subAgentSpillDir,
		PathValidator:         pathValidator,
		Workspace:             subAgentWorkspace,
		RootScopeID:           rootScopeID,
		Runs:                  runRegistry,
		ScopeManager:          scopeMgr,
		TraceStore:            traceStore,
		GuardCheck:            supervisorSvcGuardCheck(supervisorSvc),
		ResolveProviderByName: resolveProviderByName,

		// Retained lane (Plan 7 Phase 2): default on in local mode, where a
		// sub-agent's work comes back as a proposal. A container session builds
		// the runtime too, for report-lane sibling worlds; its proposal lane is
		// routed shared below because there is no host worktree to settle into.
		// agents.subagent.mode: shared remains the opt-out.
		SubNetwork:           subNetwork,
		Retain:               retainLane,
		RetainedRepoPath:     cfg.Agent.Default.RetainedRepoPath,
		RetainedWorktreeRoot: cfg.Agent.Default.RetainedWorktreeRoot,
		RetainedBootstrap:    cfg.Agent.Default.RetainedWorktreeBootstrap,
		DispatchWorlds:       dispatchWorlds,
	})

	// RoleResolver provides a live role-name lookup so the delegate
	// tool sees roles created via the role tool without a restart.
	taskTool.RoleResolver = func() []string { return subagent.DefaultRegistry().Names() }

	// LaneForRole routes each dispatch by the role's declared output
	// contract (yaah-7yf): report roles get their text back with the
	// worktree torn down (or run shared when they cannot mutate), proposal
	// roles keep the sealed-delta flow. The registry lookup is live, so
	// roles created via the role tool route immediately too.
	taskTool.LaneForRole = func(role string) tools.DelegateLane {
		switch subagent.DispatchLaneFor(subagent.SubAgentRole(role)) {
		case subagent.LaneRetainedReport:
			return tools.LaneReport
		case subagent.LaneShared:
			return tools.LaneShared
		default:
			return tools.LaneProposal
		}
	}

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
			if strings.TrimSpace(result) != "" {
				// A timeout mid-completion leaves reviewable text; delivering
				// only the error threw away everything the sub-agent wrote.
				text += "\n\npartial output before the failure:\n" + result
			}
		}
		msg := prefix + " completed:\n" + text
		select {
		case followupCh <- msg:
		case <-bgDone:
		}
	}

	toolReg.Register(taskTool)

	toolReg.Register(&tools.SubAgentJobsTool{Jobs: backgroundJobs})

	// Shepherd-dependent tool surface: the supervisor and proposal tools
	// register only when the infrastructure above came up.
	if scopeMgr != nil {
		// Supervisor tool for sub-agent supervision (list_scopes/inject/halt/status).
		toolReg.Register(&tools.SupervisorTool{
			TraceDir:     cfg.Agent.Default.TracePath(),
			RootScopeID:  rootScopeID,
			ScopeManager: scopeMgr,
			Runs:         runRegistry,
			// Svc carries the session's advisory rules service so
			// status can list rule firings (adoption plan B4); nil
			// when the supervisor stanza is not live.
			Svc: supervisorSvc,
			// supervisor.halt_root (adoption plan A3): root halt maps to
			// cancelling the session's current run — a big hammer that
			// needs the explicit opt-in, off by default. HaltRootEnabled
			// resolves the stanza over the legacy flat key.
			HaltRoot: cfg.Agent.Default.HaltRootEnabled(),
		})

		// Proposal tool (Plan 7 Phase 2): settles the sealed proposals the
		// retained lane produces. Registered only when that lane is live — a
		// shared lane or an isolated session produces none. The root-scope
		// check is a runtime guard, not a config gate: the store can fail to
		// open and the root scope can fail to be created independently of
		// the lane, and startRetainedRun refuses without a root scope.
		// taskTool.Retained is the single source of truth for "the retained
		// lane is live": it is non-nil only when the lane is enabled, the
		// scope manager initialized, and the root scope can capture. Gating
		// both the tool and the prompt on it keeps the advertised settlement
		// flow exactly in step with the registered capability.
		if taskTool.Retained != nil {
			// The orchestrator is the gate: it reads the proposal's diff and
			// settles it. There is no reviewer dispatch — the judgement lives on
			// this side of the boundary, as it does in shepherd's `gate(task,
			// predicate)`, so no verdict protocol or review round exists.
			//
			// One registry per session (yaah-yox): runner.NewDelegateTool
			// creates it with the retained runtime, and the dispatch records
			// a sealed proposal into it while the proposal tool settles it
			// back out. Passing that same registry here keeps the two halves
			// on one session, so under `web`/`acp-serve` a session can
			// neither see nor settle another session's proposals.
			toolReg.Register(&tools.ProposalTool{Proposals: taskTool.Retained.Proposals})
		} else {
			belt.Know("proposal")
		}
	} else {
		belt.Know("supervisor")

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

	// The belt's refusal log is complete now: every tool the session was
	// configured to have but does not. The model is told which are
	// missing (so it does not call an absent tool and narrate the
	// failure), and an allowlist name that matched no tool at all is a
	// typo worth reporting.
	beltRemoved := belt.Refused()

	// The prompt must never advertise a tool that is not registered.
	// Isolated mode gets the §2.2 delta instead — which names every omitted
	// tool with its reason, covers the delegate-only story, and explains
	// the apply_out workflow. The tool sections come after the last
	// registration, because the belt's list is only final there.
	if sessionSandbox != nil {
		slices.Sort(omittedTools)
		mainPrompt += "\n\n" + prompts.IsolatedWorkspaceGuidance(omittedTools, omittedMCP, beltRemoved)
	} else {
		// The guidance must never advertise a tool the belt excluded.
		if toolReg.Get("delegate") != nil {
			mainPrompt += "\n\n" + prompts.SubAgentToolsGuidance()
		}
		// RetainedModeGuidance names the proposal tool, so it is gated on the
		// same signal as that tool's registration — taskTool.Retained — rather
		// than on the config key alone. A store-open failure or an
		// uncapturable root makes the lane unavailable, and advertising
		// settlement then would promise work the runtime refuses.
		if taskTool.Retained != nil && toolReg.Get("proposal") != nil {
			mainPrompt += "\n\n" + prompts.RetainedModeGuidance()
		}
		// The belt notice is a sentence, not a list: the exact set is in
		// the request's schemas.
		if len(beltRemoved) > 0 {
			mainPrompt += "\n\n" + prompts.ToolBeltGuidance(len(beltRemoved))
		}
	}
	if opts.OneShot {
		mainPrompt += "\n\n" + prompts.OneShotGuidance()
	}
	for _, name := range belt.Unknown() {
		fmt.Fprintf(os.Stderr, "warning: config `agents.default.tools` names unknown tool %q; ignored\n", name)
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
		recorded = append(recorded, beltRemoved...)
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
		worldStatus:    worldStatus,
		cwd:            cwd,
		steerCh:        steerCh,
		rootScopeID:    rootScopeID,
		runs:           runRegistry,
		scopes:         scopeMgr,
		traceStore:     traceStore,
		traceBus:       traceBus,
		supervisor:     supervisorSvc,
		egressBroker:   egressBroker,
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
	// total (session-scoped, so never lost across turns/runs) and to
	// the session's sub-agent counter, so orchestrator and sub-agent
	// spend are separately reportable.
	backgroundJobs.OnUsage = func(u types.Usage) {
		sess.mu.Lock()
		sess.totalUsage.PromptTokens += u.PromptTokens
		sess.totalUsage.CompletionTokens += u.CompletionTokens
		sess.totalUsage.TotalTokens += u.TotalTokens
		sess.totalUsage.CachedPromptTokens += u.CacheTokens()
		sess.subUsage.PromptTokens += u.PromptTokens
		sess.subUsage.CompletionTokens += u.CompletionTokens
		sess.subUsage.TotalTokens += u.TotalTokens
		sess.subUsage.CachedPromptTokens += u.CacheTokens()
		sess.flushUsage()
		sess.mu.Unlock()
	}

	// Session-scoped sub-agent lifecycle events keep the UI's Subagents pane
	// honest for background jobs that outlive the loop that dispatched them.
	// While a loop is live its broker hooks win (no duplicate events); once
	// the loop clears them, these fallbacks deliver the completion directly
	// to the current view.
	backgroundJobs.SetSessionHooks(
		func(id, role, model, prompt string, world jobs.SubAgentWorld) {
			sess.dispatchEvent(&agent.SubAgentStartEvent{
				SubAgentID:    id,
				Role:          role,
				Model:         model,
				Prompt:        prompt,
				World:         world.Resolved,
				WorldDegraded: world.Degraded,
				WorldReason:   world.Reason,
			})
		},
		func(id, role, model, prompt, result string, dur time.Duration, err string, world jobs.SubAgentWorld) {
			sess.dispatchEvent(&agent.SubAgentEndEvent{
				SubAgentID:    id,
				Role:          role,
				Model:         model,
				Prompt:        prompt,
				Duration:      dur,
				Error:         err,
				Result:        result,
				World:         world.Resolved,
				WorldDegraded: world.Degraded,
				WorldReason:   world.Reason,
			})
		},
	)

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

// wireSubAgentNetwork lowers agents.subagent.network into the confinement policy
// a confined dispatch carries, starting the session's egress broker when the mode
// calls for one. The broker is returned so its owner can stop it; nil otherwise.
//
// The two restrictive modes are only meaningful with confinement: the rule lives
// in the jail, so a configuration that would not jail the dispatch is REFUSED at
// dispatch time (and reported by doctor) rather than silently dropped. Starting
// the broker here is unconditional so the port is known before any dispatch; the
// dispatch is where the contradiction is caught, with the config in hand.
func wireSubAgentNetwork(sc config.SubAgentConfig) (confinement.NetworkPolicy, *egress.Broker, error) {
	// The broker comes first: its port is what the policy pins to. The mapping
	// itself lives in the runner (one copy, next to the code that applies it).
	if !strings.EqualFold(strings.TrimSpace(sc.Network), "broker") {
		np, err := runner.SubAgentNetworkPolicy(sc.Network, 0)
		return np, nil, err
	}

	broker := egress.NewBroker(egress.AllowHosts(normalizeBrokerHosts(sc.NetworkHosts)...))
	port, err := broker.Start()
	if err != nil {
		return confinement.NetworkPolicy{}, nil, fmt.Errorf("agents.subagent.network: start the egress broker: %w", err)
	}
	np, err := runner.SubAgentNetworkPolicy(sc.Network, port)
	if err != nil {
		broker.Stop()
		return confinement.NetworkPolicy{}, nil, err
	}
	slog.Info("sub-agent egress broker started", "port", port, "allow_hosts", len(sc.NetworkHosts))
	return np, broker, nil
}

// normalizeBrokerHosts puts allowlist entries into the form the broker matches
// against (see egress.normalizeHost), so a padded, upper-cased or
// trailing-dot entry does exactly what it looks like. Without this an exact-host
// allowlist silently never matches.
func normalizeBrokerHosts(hosts []string) []string {
	return tools.NormalizeBrokerHosts(hosts)
}

// retainedLaneActive decides whether NewDelegateTool should build the retained
// runtime. A local session additionally needs a capturable root sandbox; a
// container session's root is pure-causal, and its report-lane siblings come
// from the session container, so retainCapable does not apply there.
func retainedLaneActive(mode string, retainCapable, containerSession bool) bool {
	if !retainedMode(mode) {
		return false
	}
	return containerSession || retainCapable
}

// retainedMode reports whether agents.subagent.mode selects the retained-run
// lane for delegate (Plan 7 Phase 2). The default (empty) is retained —
// the parity default; "shared" is the explicit opt-out.
func retainedMode(mode string) bool {
	return !strings.EqualFold(strings.TrimSpace(mode), "shared")
}
