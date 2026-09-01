package yaah

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/runner"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/jobs"
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

	cwd, _ := os.Getwd()

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

	toolReg := tools.NewRegistry()

	// Workspace containment: only enforced when --workspace is given.
	// A nil validator keeps the legacy ~-expansion behaviour.
	var pathValidator *tools.PathValidator
	if opts.WorkspaceRoot != "" {
		// Deny patterns: explicit config wins; unset falls back to the
		// built-in secret-file defaults; an explicit empty list disables.
		deny := cfg.Agent.Default.WorkspaceDenyPatterns
		if deny == nil {
			deny = defaultWorkspaceDenyPatterns
		}
		pathValidator = tools.NewPathValidator(opts.WorkspaceRoot, opts.AllowHomeAccess, deny)
		toolReg.SetPathValidator(pathValidator)
	}

	db, err := memory.OpenDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: db open failed, persistence disabled: %v\n", err)
		db = nil
	}

	// --- Embedding server ------------------------------------------------
	if db != nil {
		attachEmbedder(db, cfg)
		if db.Embedder() != nil {
			// Backfill embeddings for any memory entries created before the
			// embedding server was configured.
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
	mcpClients, mcpInfos, mcpToolNames := initMCP(cfg, toolReg, skipMCP)

	skillDirs := skillSearchPaths()
	skillTool := &tools.SkillTool{Dirs: skillDirs}
	if db != nil {
		skillTool.Embedder = db.Embedder()
	}
	toolReg.Register(skillTool)

	planDirs := planSearchPaths()
	toolReg.Register(&tools.PlanTool{Dirs: planDirs})

	roleDirs := runner.RoleSearchPaths(cwd)
	toolReg.Register(&tools.RoleTool{
		Dirs:         roleDirs,
		BuiltinFiles: runner.BuiltinRoleFiles(),
		Resolver:     runner.SubAgentRoleResolver{},
	})

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
	toolReg.Register(&tools.BackgroundProcessTool{Manager: procMgr})

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

	// Role routing: each role dispatches through exactly one sub-agent
	// tool, selected by the per-role `supervised` flag
	// (subagent.roles.<name>.supervised). Start with every role on
	// spawn_subagent; supervised roles are moved to supervised_task only
	// after shepherd infrastructure initializes successfully, so a role is
	// never unreachable when tracing is unset or init fails.
	isSupervisedRole := func(name string) bool {
		return cfg.Agent.SubAgent.Roles[name].Supervised
	}

	taskTool := runner.NewTaskTool(runner.TaskToolOpts{
		Provider:              provider,
		SystemPrompt:          systemPrompt,
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
		ToolSpillDir:          toolSpillDir(),
		PathValidator:         pathValidator,
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

	// Shepherd infrastructure: session-shared trace store, effect bus, and
	// scope manager. The orchestrator pipeline no longer hosts shepherd_trace,
	// so this is the single initialization point for the supervisor tools,
	// the supervised task tool, and sub-agent trace middleware.
	if cfg.Agent.Default.ShepherdTraceDir != "" {
		store, _, scopeMgr, serr := pipeline.InitShepherdInfrastructure(cfg.Agent.Default.ShepherdTraceDir, 0)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "warning: shepherd tracing disabled: %v\n", serr)
		} else {
			tools.SharedTraceStore = store
			tools.SharedScopeManager = scopeMgr

			// Supervised roles move from spawn_subagent to supervised_task
			// now that the shared scope manager is actually available.
			plainRoles, supervisedRoles := splitRolesBySupervised(reg.Names(), isSupervisedRole)
			taskTool.RoleNames = plainRoles
			taskTool.RoleResolver = func() []string {
				plain, _ := splitRolesBySupervised(subagent.DefaultRegistry().Names(), isSupervisedRole)
				return plain
			}
			taskTool.RoleDescriptions = runner.RoleDescriptionsFor(plainRoles)

			// Supervisor tool for sub-agent supervision (list_scopes/inject/halt/status).
			toolReg.Register(&tools.SupervisorTool{TraceDir: cfg.Agent.Default.ShepherdTraceDir})

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
			})
		}
	}

	// Advertise supervised_task only when it was actually registered (the
	// shared scope manager is set exclusively on successful init), so the
	// prompt never references a tool that init failure left unavailable.
	if tools.SharedScopeManager != nil {
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
		cwd:            cwd,
		steerCh:        make(chan string, 4),
		followupCh:     followupCh,
	}

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
