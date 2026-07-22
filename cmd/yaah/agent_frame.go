package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/observability"
	processpkg "github.com/buchenberg/yaah/internal/process"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/spinner"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// agentSession holds the long-lived infrastructure shared across REPL and
// one-shot prompts. Building it once avoids re-opening the database,
// re-spawning MCP servers, and re-discovering skills on every turn.
type agentSession struct {
	cfg          *config.Config
	provider     agent.Provider
	providerName string
	modelName    string
	systemPrompt string
	toolReg      *tools.Registry
	db           *memory.DB
	mcpClients   []mcp.MCPClient
	procMgr      *processpkg.Manager
	messages     []types.Message
	sessionID    string
	msgIdx       int
	otelShutdown func(context.Context) error
	tracker      *tools.ConflictTracker
}

func newAgentSession() (*agentSession, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	provider := resolveProvider(cfg)
	modelName := resolveModel(cfg)
	providerName := resolveProviderName(cfg)

	// Initialise OpenTelemetry if configured.
	otelShutdown := func(_ context.Context) error { return nil }
	if cfg.Observability.Otel.Enabled {
		otelCfg := observability.Config{
			Enabled:     true,
			Endpoint:    cfg.Observability.Otel.Endpoint,
			ServiceName: cfg.Observability.Otel.ServiceName,
			Traces:      cfg.Observability.Otel.Traces,
			Metrics:     cfg.Observability.Otel.Metrics,
		}
		if otelCfg.Endpoint == "" {
			otelCfg.Endpoint = "localhost:4317"
		}
		if ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); ep != "" {
			otelCfg.Endpoint = ep
		}
		if os.Getenv("YAAH_OTEL_ENABLED") == "true" {
			otelCfg.Enabled = true
		}
		if otelCfg.ServiceName == "" {
			otelCfg.ServiceName = "yaah"
		}
		sd, err := observability.Setup(context.Background(), otelCfg)
		if err != nil {
			return nil, fmt.Errorf("otel: %w", err)
		}
		otelShutdown = sd
	}

	cwd, _ := os.Getwd()

	// Load sub-agent role definitions: built-in (embedded) +
	// user-defined (~/.agents/roles/, ./.agents/roles/).
	reg := subagent.NewRoleRegistry()
	if files := builtinRoleFiles(); files != nil {
		reg.LoadBytes(files)
	}
	for _, dir := range roleSearchPaths(cwd) {
		reg.LoadDir(dir)
	}
	subagent.SetDefaultRoleRegistry(reg)

	layers := prompts.Layers{
		Identity:    prompts.IdentityPrompt,
		Environment: prompts.DetectEnvironment(cwd),
		UserContext: prompts.LoadUserContext(config.HomeDir()),
		Project:     instructions.FormatForSystem(instructions.Load(cwd, cwd)),
	}

	toolReg := tools.NewRegistry()

	db, err := memory.OpenDefault()
	if err == nil {
		if entries, memErr := db.ListMemory(50); memErr == nil && len(entries) > 0 {
			var memLines []string
			for _, entry := range entries {
				memLines = append(memLines, "- "+entry.Text)
			}
			layers.Memory = "You have the following stored information about the user and project:\n" + strings.Join(memLines, "\n")
		}

		toolReg.Register(&tools.MemorySearchTool{DB: db})
		toolReg.Register(&tools.MemoryAddTool{DB: db})
		toolReg.Register(&tools.MemoryDeleteTool{DB: db})
		toolReg.Register(&tools.MemoryUpdateTool{DB: db})
		toolReg.Register(&tools.MemorySessionSearchTool{DB: db})
	}

	systemPrompt := prompts.Build(layers)

	mcpDirs := mcpSearchPaths(config.HomeDir())
	mcpClients, mcpTools, _, mcpErr := mcp.StartMCPClientsWithStderr(context.Background(), mcpDirs, io.Discard)
	if mcpErr != nil {
		fmt.Fprintf(os.Stderr, "warning: MCP startup error: %v\n", mcpErr)
	}
	for _, t := range mcpTools {
		toolReg.Register(t)
	}

	skillDirs := skillSearchPaths()
	toolReg.Register(&tools.SkillTool{Dirs: skillDirs})

	planDirs := planSearchPaths()
	toolReg.Register(&tools.PlanTool{Dirs: planDirs})

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
	if resumeSessionID != "" {
		if db == nil {
			return nil, fmt.Errorf("cannot resume session: no database available (run 'yaah doctor')")
		}
		dbMsgs, err := db.GetMessages(resumeSessionID)
		if err != nil {
			return nil, fmt.Errorf("cannot resume session %s: %w", resumeSessionID, err)
		}
		if len(dbMsgs) == 0 {
			return nil, fmt.Errorf("session %s not found or has no messages", resumeSessionID)
		}
		messages = make([]types.Message, 0, len(dbMsgs))
		for _, m := range dbMsgs {
			msg := types.Message{
				Role:    m.Role,
				Content: m.Content,
				Name:    m.ToolName,
			}
			if m.ToolCalls != "" {
				json.Unmarshal([]byte(m.ToolCalls), &msg.ToolCalls)
			}
			messages = append(messages, msg)
		}
		if len(messages) > 0 {
			last := messages[len(messages)-1]
			if last.Role == "assistant" && len(last.ToolCalls) > 0 {
				messages = append(messages, types.SystemMsg(
					"Previous execution was interrupted. Please continue from where you left off."))
			}
		}
		sessionID = resumeSessionID
		msgIdx = len(dbMsgs)
	} else {
		sessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
		if db != nil {
			cwd, _ := os.Getwd()
			db.CreateSession(memory.Session{
				ID:        sessionID,
				StartedAt: time.Now().Unix(),
				CWD:       cwd,
				Model:     modelName,
			})
		}
	}

	tracker := &tools.ConflictTracker{}
	subAgentProvider, subAgentModel := resolveSubAgent(cfg)
	toolReg.Register(newTaskTool(provider, systemPrompt, modelName, db, sessionID, subAgentProvider, subAgentModel, cfg.Agent.SubAgent, reg.Names(), cfg.Observability.Otel.Enabled, cfg.Observability.Otel.Verbose, tracker))

	toolReg.Register(&tools.ListSubAgentsTool{
		Lister: func() []tools.SubAgentInfo {
			defs := reg.List()
			infos := make([]tools.SubAgentInfo, 0, len(defs))
			for name, def := range defs {
				desc := def.Body
				if idx := strings.IndexByte(desc, '\n'); idx >= 0 {
					desc = desc[:idx]
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

	// Wrap the provider with OTel instrumentation if enabled.
	if cfg.Observability.Otel.Enabled {
		if sp, ok := provider.(agent.StreamProvider); ok {
			provider = &observability.InstrumentedProvider{Inner: sp, Verbose: cfg.Observability.Otel.Verbose}
		}
	}

	return &agentSession{
		cfg:          cfg,
		provider:     provider,
		providerName: providerName,
		modelName:    modelName,
		systemPrompt: systemPrompt,
		toolReg:      toolReg,
		db:           db,
		mcpClients:   mcpClients,
		procMgr:      procMgr,
		sessionID:    sessionID,
		messages:     messages,
		msgIdx:       msgIdx,
		otelShutdown: otelShutdown,
		tracker:      tracker,
	}, nil
}

func (s *agentSession) close() {
	ctx := context.Background()
	if s.otelShutdown != nil {
		s.otelShutdown(ctx)
	}
	if s.db != nil {
		s.db.EndSession(s.sessionID, time.Now().Unix())
		s.db.Close()
	}
	for _, c := range s.mcpClients {
		c.Close()
	}
}

func (s *agentSession) compactContext() {
	if len(s.messages) <= 4 {
		fmt.Fprintf(os.Stderr, "  %s\n", Dim("context is already small enough"))
		return
	}

	window := s.cfg.Agent.Default.ContextWindow
	if window <= 0 {
		window = 128000
	}

	totalChars := 0
	for _, m := range s.messages {
		totalChars += len(m.Content)
	}
	estTokens := totalChars / 4
	target := window * 4 / 5

	if estTokens <= target {
		fmt.Fprintf(os.Stderr, "  %s %d/%d tokens (%d%%)\n",
			Dim("context:"), estTokens, window, estTokens*100/window)
		return
	}

	fmt.Fprintf(os.Stderr, "  %s %d/%d tokens (%d%%) — compacting...\n",
		Dim("context:"), estTokens, window, estTokens*100/window)

	sysMsg := s.messages[0]
	rest := s.messages[1:]

	split := len(rest) / 2
	oldMsgs := rest[:split]
	keepMsgs := rest[split:]

	var sb strings.Builder
	sb.WriteString("Summarize the following conversation excerpt in 2-3 sentences. Be concise and factual.\n\n")
	for _, m := range oldMsgs {
		if m.Content != "" {
			sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
		}
	}

	req := types.ChatRequest{
		Model: s.modelName,
		Messages: []types.Message{
			types.UserMsg(sb.String()),
		},
	}

	resp, err := s.provider.Send(context.Background(), req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s\n", replYellow("compact failed: "+err.Error()))
		return
	}

	if len(resp.Choices) == 0 {
		fmt.Fprintf(os.Stderr, "  %s\n", replYellow("compact failed: no response"))
		return
	}

	summary := resp.Choices[0].Message.Content
	newMsgs := []types.Message{sysMsg}
	newMsgs = append(newMsgs, types.SystemMsg("Previous conversation summary: "+summary))
	newMsgs = append(newMsgs, keepMsgs...)
	s.messages = newMsgs

	newTokens := 0
	for _, m := range s.messages {
		newTokens += len(m.Content) / 4
	}
	fmt.Fprintf(os.Stderr, "  %s %d/%d tokens (%d%%)\n",
		Dim("compacted:"), newTokens, window, newTokens*100/window)
}

// runPrompt executes a single agent prompt with the session's shared
// infrastructure and per-turn callbacks (spinner, streaming display).
func (s *agentSession) runPrompt(prompt string) (string, bool, error) {
	compactProvider, compactModel := resolveCompact(s.cfg)

	executorProvider, executorModel := resolveExecutor(s.cfg)

	fallbackProvider, fallbackModel := resolveFallback(s.cfg)

	loop := &agent.Loop{
		Provider:               s.provider,
		CompactProvider:        compactProvider,
		CompactModel:           compactModel,
		ExecutorProvider:       executorProvider,
		ExecutorModel:          executorModel,
		MaxInnerIterations:     s.cfg.Agent.Executor.MaxIterations,
		FallbackProvider:       fallbackProvider,
		FallbackModel:          fallbackModel,
		Registry:               s.toolReg,
		Model:                  s.modelName,
		SystemPrompt:           s.systemPrompt,
		MaxInlineToolsPerTurn:  s.cfg.Agent.Default.MaxInlineToolsPerTurn,
		MaxIterations:          s.cfg.Agent.Default.MaxIterations,
		ContextWindow:          s.cfg.Agent.Default.ContextWindow,
		ApprovalMode:           resolveApproval(s.cfg),
		Messages:               s.messages,
		HookDir:                s.cfg.Hooks.Dir,
		SessionID:              s.sessionID,
		PipelineNames:          s.cfg.Agent.Middleware.Enabled,
		PipelineDisabled:       s.cfg.Agent.Middleware.Disabled,
		DB:                     s.db,
		MsgIdx:                 s.msgIdx,
		MaxSubAgentDepth:       s.cfg.Agent.SubAgent.MaxDepth,
		MaxSubAgentConcurrency: s.cfg.Agent.SubAgent.MaxConcurrency,
		MaxSubAgentDepthByRole: subAgentDepthByRole(s.cfg.Agent.SubAgent),
		OtelEnabled:            s.cfg.Observability.Otel.Enabled,
		OtelVerbose:            s.cfg.Observability.Otel.Verbose,
		ConflictTracker:        s.tracker,
		ToolsLevel:             agent.FullTools,
	}

	spin := spinner.New(nil, "Thinking...")
	fmt.Fprintln(os.Stderr)
	spin.Start()

	streamed := false
	loop.OnToken = func(token string) {
		if !streamed {
			spin.Stop()
			fmt.Fprintln(os.Stderr)
			streamed = true
		}
		fmt.Fprint(os.Stderr, token)
	}

	loop.OnTool = func(info agent.ToolInfo) {
		if info.Duration == 0 {
			spin.Stop()
			return
		}
		if info.Name == "spawn_subagent" {
			return // sub-agent lifecycle rendered by OnSubAgent
		}

		fmt.Fprintf(os.Stderr, "\n  tool: %s", Bold(info.Name))
		if info.Args != "" {
			args := info.Args
			if len(args) > 60 {
				args = args[:57] + "..."
			}
			fmt.Fprintf(os.Stderr, "(%s)", Dim(args))
		}
		fmt.Fprintf(os.Stderr, " (%s)\n", Dim(formatDuration(info.Duration)))
		if info.Error != "" {
			fmt.Fprintf(os.Stderr, "    %s\n", replYellow("error: "+info.Error))
		}
	}

	loop.OnSubAgent = func(info agent.SubAgentInfo) {
		displayName := subagent.RoleDisplayName(subagent.SubAgentRole(info.Role))
		specialty := subagent.RoleSpecialty(subagent.SubAgentRole(info.Role))
		label := displayName
		if specialty != "" {
			label += " — " + specialty
		}
		if info.Duration == 0 {
			fmt.Fprintf(os.Stderr, "\n╭─ sub-agent: %s · %s\n", Bold(label), info.Prompt)
		} else {
			status := "completed"
			if info.Error != "" {
				status = replYellow(info.Error)
			}
			modelStr := ""
			if info.Model != "" {
				modelStr = " [" + Dim(info.Model) + "]"
			}
			fmt.Fprintf(os.Stderr, "╰─ sub-agent: %s%s · %s (%s)\n", Bold(label), modelStr, status, Dim(formatDuration(info.Duration)))
		}
	}

	response, err := loop.Run(context.Background(), prompt)
	if !streamed {
		spin.Stop()
	}

	if streamed {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr)
	}

	// Persist the conversation history for the next turn.
	s.messages = loop.Messages
	s.msgIdx = loop.MsgIdx

	return response, streamed, err
}
