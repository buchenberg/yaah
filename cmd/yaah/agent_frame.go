package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
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
	"github.com/buchenberg/yaah/internal/skills"
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
	mcpInfos     []mcp.ServerInfo
	procMgr      *processpkg.Manager
	messages     []types.Message
	sessionID    string
	msgIdx       int
	otelShutdown func(context.Context) error
	tracker      *tools.ConflictTracker

	view      agent.View
	ctrlCh    chan<- types.CtrlMsg
	approveFn func(name, args string) bool
	mu        sync.RWMutex

	steerCh    chan string
	followupCh chan string
}

func newAgentSession() (*agentSession, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	provider := resolveProvider(cfg)
	modelName := resolveModel(cfg)
	providerName := resolveProviderName(cfg)

	// Initialise OpenTelemetry if configured. Serve mode injects
	// extraOtelProcessors (an in-memory BufferingSpanProcessor) and sets
	// otelInMemoryOnly so tracing activates without an OTLP endpoint.
	otelShutdown := func(_ context.Context) error { return nil }
	otelActive := cfg.Observability.Otel.Enabled || len(extraOtelProcessors) > 0
	if otelActive {
		otelCfg := observability.Config{
			Enabled:         true,
			Endpoint:        cfg.Observability.Otel.Endpoint,
			ServiceName:     cfg.Observability.Otel.ServiceName,
			Traces:          true,
			Metrics:         cfg.Observability.Otel.Metrics,
			ExtraProcessors: extraOtelProcessors,
		}
		if otelInMemoryOnly {
			// No OTLP exporter — spans flow only to the in-memory buffer.
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
		Identity:               prompts.IdentityPrompt,
		Environment:            prompts.DetectEnvironment(cwd),
		UserContext:            prompts.LoadUserContext(config.HomeDir()),
		Project:                instructions.FormatForSystem(instructions.Load(cwd, cwd)),
		MaxSubAgentConcurrency: cfg.Agent.SubAgent.MaxConcurrency,
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
	if err == nil {
		systemPrompt += "\n\n## Memory Guidelines\n- Use memory_search to find relevant memories before answering personal/project questions. Pass a tag to filter by category.\n- When the user asks about past conversations or session history, use memory_search_sessions with an empty query to list recent transcripts.\n- Use memory_add to save important facts. Always include a tags array (e.g., [\"user_info\"], [\"preferences\"], [\"project:yaah\"], [\"decision\"]).\n- Use memory_update to correct stale facts (requires the memory ID). Use memory_delete to remove incorrect memories.\n- At the end of a conversation or when the user says goodbye, use memory_add to save a 2-3 line summary of key discussion points with tag [\"session_summary\"]."
	}

	mcpDirs := mcpSearchPaths(config.HomeDir())
	mcpClients, mcpTools, mcpInfos, mcpErr := mcp.StartMCPClientsWithStderr(context.Background(), mcpDirs, io.Discard)
	if mcpErr != nil {
		fmt.Fprintf(os.Stderr, "warning: MCP startup error: %v\n", mcpErr)
	}
	for _, t := range mcpTools {
		toolReg.Register(t)
	}

	skillDirs := skillSearchPaths()
	toolReg.Register(&tools.SkillTool{Dirs: skillDirs})

	if discovered := skills.Discover(skillDirs); len(discovered) > 0 {
		layers.Skills = prompts.BuildSkillsIndex(discovered)
	}

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
				Role:       m.Role,
				Content:    m.Content,
				Name:       m.ToolName,
				ToolCallID: m.ToolCallID,
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
	subCW := cfg.Agent.Default.ContextWindow / 2
	if subCW < 32000 {
		subCW = 32000
	}
	toolReg.Register(newTaskTool(provider, systemPrompt, modelName, db, sessionID, subAgentProvider, subAgentModel, cfg.Agent.SubAgent, reg.Names(), cfg.Observability.Otel.Enabled, cfg.Observability.Otel.Verbose, tracker, cfg.Agent.Default.EstimateFactor, subCW, cfg.Agent.SubAgent.OutputLimit, cfg.Providers, cfg.Agent.Default))

	toolReg.Register(&tools.ListSubAgentsTool{
		Lister: func() []tools.SubAgentInfo {
			defs := reg.List()
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

	// Wrap the provider with OTel instrumentation if enabled.
	if otelActive {
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
		mcpInfos:     mcpInfos,
		procMgr:      procMgr,
		sessionID:    sessionID,
		messages:     messages,
		msgIdx:       msgIdx,
		otelShutdown: otelShutdown,
		tracker:      tracker,
		steerCh:      make(chan string, 4),
		followupCh:   make(chan string, 32),
	}, nil
}

func (s *agentSession) close() {
	ctx := context.Background()
	if s.steerCh != nil {
		close(s.steerCh)
	}
	if s.followupCh != nil {
		close(s.followupCh)
	}
	if s.ctrlCh != nil {
		s.ctrlCh <- &types.CtrlDone{}
	}
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

func (s *agentSession) SetView(v agent.View) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.view = v
}

func (s *agentSession) SetCtrlCh(ch chan<- types.CtrlMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctrlCh = ch
}

func (s *agentSession) SetApproveFn(fn func(name, args string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approveFn = fn
}

func (s *agentSession) SetModel(providerName, modelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prov := s.provider
	if p, ok := s.cfg.Providers[providerName]; ok {
		if pv, ok2 := makeProvider(p); ok2 {
			prov = pv
		}
	}
	s.provider = prov
	s.providerName = providerName
	s.modelName = modelName
}

func (s *agentSession) SetSystemPrompt(prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.systemPrompt = prompt
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

// terminalView implements agent.View for REPL terminal output.
// It prints tokens, tool calls, and sub-agent lifecycle events to stderr
// using the same formatting as the previous callback-based REPL.
type terminalView struct{}

func (terminalView) HandleEvent(evt agent.Event) {
	switch e := evt.(type) {
	case *agent.TokenDeltaEvent:
		fmt.Fprint(os.Stderr, e.Text)
	case *agent.ToolStartEvent:
		// handled on ToolEndEvent
	case *agent.ToolEndEvent:
		if e.Name == "spawn_subagent" {
			return
		}
		fmt.Fprintf(os.Stderr, "\n  tool: %s", Bold(e.Name))
		if e.Args != "" {
			args := e.Args
			if len(args) > 60 {
				args = args[:57] + "..."
			}
			fmt.Fprintf(os.Stderr, "(%s)", Dim(args))
		}
		fmt.Fprintf(os.Stderr, " (%s)\n", Dim(formatDuration(e.Duration)))
		if e.Error != "" {
			fmt.Fprintf(os.Stderr, "    %s\n", replYellow("error: "+e.Error))
		}
	case *agent.SubAgentStartEvent:
		displayName := subagent.RoleDisplayName(subagent.SubAgentRole(e.Role))
		specialty := subagent.RoleSpecialty(subagent.SubAgentRole(e.Role))
		label := displayName
		if specialty != "" {
			label += " — " + specialty
		}
		fmt.Fprintf(os.Stderr, "\n╭─ sub-agent: %s · %s\n", Bold(label), e.Prompt)
	case *agent.SubAgentEndEvent:
		displayName := subagent.RoleDisplayName(subagent.SubAgentRole(e.Role))
		specialty := subagent.RoleSpecialty(subagent.SubAgentRole(e.Role))
		label := displayName
		if specialty != "" {
			label += " — " + specialty
		}
		status := "completed"
		if e.Error != "" {
			status = replYellow(e.Error)
		}
		modelStr := ""
		if e.Model != "" {
			modelStr = " [" + Dim(e.Model) + "]"
		}
		fmt.Fprintf(os.Stderr, "╰─ sub-agent: %s%s · %s (%s)\n", Bold(label), modelStr, status, Dim(formatDuration(e.Duration)))
	}
}

// runPrompt executes a single agent prompt with the session's shared
// infrastructure. The optional view argument overrides s.view; if both are
// nil, terminalView is used (REPL behavior).
func (s *agentSession) runPrompt(prompt string, view ...agent.View) (string, bool, error) {
	compactProvider, compactModel := resolveCompact(s.cfg)
	fallbackProvider, fallbackModel := resolveFallback(s.cfg)

	s.mu.RLock()
	prov := s.provider
	mName := s.modelName
	v := s.view
	ctrl := s.ctrlCh
	appr := s.approveFn
	s.mu.RUnlock()

	spin := spinner.New(nil, "Thinking...")
	fmt.Fprintln(os.Stderr)
	spin.Start()

	streamed := false
	var inner agent.View
	if len(view) > 0 && view[0] != nil {
		inner = view[0]
	} else if v != nil {
		inner = v
	} else {
		inner = terminalView{}
	}
	var spinnerView agent.View = &replView{
		inner:        inner,
		onFirstToken: func() {
			if !streamed {
				streamed = true
				spin.Stop()
				fmt.Fprintln(os.Stderr)
			}
		},
	}

	loop := agent.NewLoop(prov, s.toolReg,
		agent.WithModel(mName),
		agent.WithSystemPrompt(s.systemPrompt),
		agent.WithView(spinnerView),
		agent.WithMessages(s.messages),
		agent.WithDB(s.db),
		agent.WithWriteDebouncer(func() *memory.DebouncedWriter {
			if s.db != nil {
				return memory.NewDebouncedWriter(s.db)
			}
			return nil
		}()),
		agent.WithSessionID(s.sessionID),
		agent.WithMsgIdx(s.msgIdx),
		agent.WithHookDir(s.cfg.Hooks.Dir),
		agent.WithFallback(fallbackProvider, fallbackModel),
		agent.WithCompactProvider(compactProvider, compactModel),
		agent.WithPipeline(s.cfg.Agent.Middleware.Enabled, s.cfg.Agent.Middleware.Disabled),
		agent.WithSteer(s.steerCh),
		agent.WithFollowUps(s.followupCh),
		agent.WithConflictTracker(s.tracker),
		agent.WithToolsLevel(agent.FullTools),
		agent.WithOtel(s.cfg.Observability.Otel.Enabled, s.cfg.Observability.Otel.Verbose),
		agent.WithSubAgentConcurrency(
			s.cfg.Agent.SubAgent.MaxConcurrency,
			time.Duration(s.cfg.Agent.SubAgent.StuckChildTimeout)*time.Second,
			buildStuckChildTimeouts(s.cfg.Agent.SubAgent),
		),
		agent.WithLoopConfig(agent.LoopConfig{
			MaxIterations:          s.cfg.Agent.Default.MaxIterations,
			MaxTurns:               s.cfg.Agent.Default.MaxTurns,
			MaxRetries:             s.cfg.Agent.Default.MaxRetries,
			RetryBackoffSecs:       s.cfg.Agent.Default.RetryBackoffSecs,
			ContextWindow:          s.cfg.Agent.Default.ContextWindow,
			CompactionThreshold:    s.cfg.Agent.Default.CompactionThreshold,
			RawCompactionThreshold: s.cfg.Agent.Default.RawCompactionThreshold,
			EstimateFactor:         s.cfg.Agent.Default.EstimateFactor,
			LoopDetectCount:        s.cfg.Agent.Default.LoopDetectCount,
			LoopDetectWindow:       s.cfg.Agent.Default.LoopDetectWindow,
			MaxToolConcurrency:     s.cfg.Agent.Default.MaxToolConcurrency,
			MaxInlineToolsPerTurn:  s.cfg.Agent.Default.MaxInlineToolsPerTurn,
			PromptCaching:          s.cfg.Agent.Default.PromptCaching,
			ReasoningProtectTurns:  s.cfg.Agent.Default.ReasoningProtect,
			ToolResultMaxLines:     s.cfg.Agent.Default.ToolResultMaxLines,
			ToolResultMaxBytes:     s.cfg.Agent.Default.ToolResultMaxBytes,
			PruneProtectTokens:     s.cfg.Agent.Default.PruneProtectTokens,
			PruneMinReclaim:        s.cfg.Agent.Default.PruneMinReclaim,
			PruneMinTurns:          s.cfg.Agent.Default.PruneMinTurns,
		}),
		agent.WithApprovalMode(resolveApproval(s.cfg)),
	)

	if appr != nil {
		loop.ApproveFn = appr
	}

	response, err := loop.Run(context.Background(), prompt)

	if !streamed {
		spin.Stop()
	}
	if streamed {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr)
	}

	s.messages = loop.Messages
	s.msgIdx = loop.MsgIdx

	if ctrl != nil {
		if err != nil {
			select {
			case ctrl <- &types.CtrlError{Err: err}:
			default:
			}
		}
		select {
		case ctrl <- &types.CtrlContextInfo{
			Tokens: loop.EstimatedTokens(),
			Window: loop.ContextWindow,
		}:
		default:
		}
	}

	return response, streamed, err
}

// replView wraps a terminalView and triggers onFirstToken on the first token.
type replView struct {
	inner        agent.View
	onFirstToken func()
	firstOnce    sync.Once
}

func (v *replView) HandleEvent(evt agent.Event) {
	if _, ok := evt.(*agent.TokenDeltaEvent); ok {
		v.firstOnce.Do(v.onFirstToken)
	}
	v.inner.HandleEvent(evt)
}
