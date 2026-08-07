package yaah

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/runner"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/observability"
	processpkg "github.com/buchenberg/yaah/internal/process"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/skills"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func newAgentSession() (*agentSession, error) {
	return newAgentSessionWithOptions(false, false)
}

// newAgentSessionWithOptions creates an agent session. When skipMCP is true,
// MCP server subprocesses are not started (they consume CPU and make tview
// input sluggish on Windows). When skipOtel is true, OTel exporters are
// not initialised.
func newAgentSessionWithOptions(skipMCP, skipOtel bool) (*agentSession, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	provider := resolveProvider(cfg)
	modelName := resolveModel(cfg)
	providerName := resolveProviderName(cfg)

	// --- OpenTelemetry ---------------------------------------------------
	var otelShutdown func(context.Context) error
	var otelActive bool
	otelShutdown, otelActive, err = initOtel(cfg, skipOtel)
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

	layers := prompts.Layers{
		Identity:               prompts.IdentityPrompt,
		Environment:            prompts.DetectEnvironment(cwd),
		UserContext:            prompts.LoadUserContext(config.HomeDir()),
		Project:                instructions.FormatForSystem(instructions.Load(cwd, cwd)),
		MaxSubAgentConcurrency: cfg.Agent.SubAgent.MaxConcurrency,
	}

	toolReg := tools.NewRegistry()

	// Workspace containment: only enforced when --workspace is given.
	// A nil validator keeps the legacy ~-expansion behaviour.
	var pathValidator *tools.PathValidator
	if workspaceRoot != "" {
		pathValidator = tools.NewPathValidator(workspaceRoot, allowHomeAccess, nil)
		toolReg.SetPathValidator(pathValidator)
	}

	db, err := memory.OpenDefault()
	if err == nil {
		if entries, memErr := db.ListMemory(50); memErr == nil && len(entries) > 0 {
			var memLines []string
			for _, entry := range entries {
				if strings.Contains(entry.Tags, `"user_info"`) {
					continue
				}
				memLines = append(memLines, "- "+entry.Text)
			}
			if len(memLines) > 0 {
				layers.Memory = "You have the following stored information about the user and project:\n" + strings.Join(memLines, "\n")
			}
		}

		toolReg.Register(&tools.MemorySearchTool{DB: db})
		toolReg.Register(&tools.MemoryAddTool{DB: db})
		toolReg.Register(&tools.MemoryDeleteTool{DB: db})
		toolReg.Register(&tools.MemoryUpdateTool{DB: db})
		toolReg.Register(&tools.MemorySessionSearchTool{DB: db})
	}

	systemPrompt := prompts.Build(layers)
	if err == nil && resumeSessionID == "" {
		systemPrompt += "\n\n## Memory Guidelines\n- Use memory_search to find relevant memories before answering personal/project questions. Pass a tag to filter by category.\n- When the user asks about past conversations or session history, use memory_search_sessions with an empty query to list recent transcripts.\n- Use memory_add to save important facts. Always include a tags array (e.g., [\"user_info\"], [\"preferences\"], [\"project:yaah\"], [\"decision\"]).\n- Use memory_update to correct stale facts (requires the memory ID). Use memory_delete to remove incorrect memories.\n- At the end of a conversation or when the user says goodbye, use memory_add to save a 2-3 line summary of key discussion points with tag [\"session_summary\"]."
	}

	// --- MCP servers ------------------------------------------------------
	mcpClients, mcpInfos := initMCP(cfg, toolReg, skipMCP)

	skillDirs := skillSearchPaths()
	toolReg.Register(&tools.SkillTool{Dirs: skillDirs})

	if discovered := skills.Discover(skillDirs); len(discovered) > 0 {
		layers.Skills = prompts.BuildSkillsIndex(discovered)
	}

	planDirs := planSearchPaths()
	toolReg.Register(&tools.PlanTool{Dirs: planDirs})

	roleDirs := runner.RoleSearchPaths(cwd)
	toolReg.Register(&tools.RoleTool{
		Dirs:         roleDirs,
		BuiltinFiles: runner.BuiltinRoleFiles(),
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

	messages, sessionID, msgIdx, systemPrompt, err = restoreSession(db, resumeSessionID, systemPrompt)
	if err != nil {
		return nil, err
	}
	if resumeSessionID == "" {
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

	// Default directives apply to the top-level agent only, injected
	// right after the identity block. systemPrompt (the sub-agent base)
	// stays clean so child prompts never inherit them.
	mainPrompt := prompts.InjectAfterIdentity(systemPrompt, resolveDirectives(cfg))

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
	backgroundJobs := tools.NewBackgroundJobs()
	backgroundJobs.MaxConcurrent = cfg.Agent.SubAgent.MaxConcurrency
	if backgroundJobs.MaxConcurrent <= 0 {
		backgroundJobs.MaxConcurrent = 4
	}

	taskTool := runner.NewTaskTool(provider, systemPrompt, modelName, db, sessionID, subAgentProvider, subAgentModel, cfg.Agent.SubAgent, reg.Names(), cfg.Observability.Otel.Enabled, cfg.Observability.Otel.Verbose, tracker, cfg.Agent.Default.EstimateFactor, subCW, cfg.Agent.SubAgent.OutputLimit, cfg.Providers, cfg.Agent.Default, nil, pathValidator, resolveProviderByName)

	// RoleResolver provides a live role-name lookup so the spawn_subagent
	// tool sees roles created via the role tool without a restart. The
	// cached reg.Names() snapshot above is layered underneath.
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
				infos = append(infos, tools.SubAgentInfo{
					Role:        string(name),
					DisplayName: def.DisplayName,
					Specialty:   def.Specialty,
					Contract: tools.SubAgentContract{
						Heading: def.Contract.Heading,
						Fields:  def.Contract.Fields,
					},
					Description: desc,
					Tools:       def.Tools,
				})
			}
			return infos
		},
	})

	// Build and inject a compact tool quick-reference card so the
	// model has signature-first parameter info near the top of the
	// prompt, rather than only verbose JSON schemas at the bottom.
	if quickRef := buildToolQuickRef(toolReg); quickRef != "" {
		mainPrompt += "\n\n" + quickRef
	}

	// Wrap the provider with OTel instrumentation if enabled.
	if otelActive {
		if sp, ok := provider.(agent.StreamProvider); ok {
			provider = &observability.InstrumentedProvider{Inner: sp, Verbose: cfg.Observability.Otel.Verbose}
		}
	}

	sess := &agentSession{
		cfg:            cfg,
		provider:       provider,
		providerName:   providerName,
		modelName:      modelName,
		systemPrompt:   systemPrompt,
		mainPrompt:     mainPrompt,
		toolReg:        toolReg,
		db:             db,
		mcpClients:     mcpClients,
		mcpInfos:       mcpInfos,
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
	if pathValidator != nil && (workspaceAsk || cfg.Agent.Default.WorkspaceAsk) {
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

// --- helpers ---------------------------------------------------------------

// initOtel initialises OpenTelemetry when configured. Serve mode injects
// extraOtelProcessors (an in-memory BufferingSpanProcessor) and sets
// otelInMemoryOnly so tracing activates without an OTLP endpoint.
func initOtel(cfg *config.Config, skipOtel bool) (func(context.Context) error, bool, error) {
	noop := func(_ context.Context) error { return nil }
	if skipOtel || (!cfg.Observability.Otel.Enabled && len(extraOtelProcessors) == 0) {
		return noop, false, nil
	}
	otelCfg := observability.Config{
		Enabled:         true,
		Endpoint:        cfg.Observability.Otel.Endpoint,
		ServiceName:     cfg.Observability.Otel.ServiceName,
		Traces:          true,
		Metrics:         cfg.Observability.Otel.Metrics,
		ExtraProcessors: extraOtelProcessors,
	}
	if otelInMemoryOnly {
		otelCfg.Endpoint = ""
		otelCfg.Metrics = false
	} else {
		if otelCfg.Endpoint == "" {
			otelCfg.Endpoint = "localhost:4317"
		}
		if ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); ep != "" {
			otelCfg.Endpoint = ep
		}
	}
	if os.Getenv("YAAH_OTEL_ENABLED") == "true" {
		otelCfg.Enabled = true
	}
	if otelCfg.ServiceName == "" {
		otelCfg.ServiceName = "yaah"
	}
	sd, err := observability.Setup(context.Background(), otelCfg)
	if err != nil {
		return nil, false, fmt.Errorf("otel: %w", err)
	}
	return sd, true, nil
}

// initMCP starts configured MCP servers, registers their tools into
// toolReg, and returns client handles and server info. Start errors
// are non-fatal and reported to stderr.
func initMCP(cfg *config.Config, toolReg *tools.Registry, skipMCP bool) ([]mcp.MCPClient, []mcp.ServerInfo) {
	if skipMCP {
		return nil, nil
	}
	mcpManifests := make(map[string]*mcp.Manifest)
	for name, s := range cfg.MCPServers {
		mcpManifests[name] = &mcp.Manifest{
			Command:   s.Command,
			Args:      s.Args,
			Env:       s.Env,
			URL:       s.URL,
			Transport: s.Transport,
			Framing:   s.Framing,
			Headers:   s.Headers,
		}
	}
	clients, mcpTools, infos, err := mcp.StartMCPClientsFromConfig(context.Background(), mcpManifests, io.Discard)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: MCP startup error: %v\n", err)
	}
	for _, t := range mcpTools {
		toolReg.Register(t)
	}
	return clients, infos
}
