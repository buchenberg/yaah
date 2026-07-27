package yaah

// UI Driver Pattern
//
// To attach a new UI to a session:
//
//  1. Create a chan types.CtrlMsg and call sess.SetCtrlCh(ch).
//     This wires todos and status messages to your channel.
//
//  2. Implement agent.View (HandleEvent) and call sess.SetView(your view).
//     Events: TokenDelta, Thinking, Flush, ToolStart, ToolEnd,
//             SubAgentStart, SubAgentEnd, Done.
//
//  3. Read from the control channel in a goroutine and dispatch
//     CtrlStatus, CtrlError, CtrlQuestion, CtrlApproval,
//     CtrlModelList, CtrlTodos, CtrlContextInfo, CtrlDone.
//
//  4. Call sess.RunPrompt(ctx, text) for each turn.
//     ctx cancellation aborts the in-flight agent loop.
//
// See: terminalView (REPL), agentViewFwd (Bubble Tea TUI).

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
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/skills"
	"github.com/buchenberg/yaah/internal/spinner"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Session is the stable contract between UI drivers and the shared
// agent session. Any driver (TUI, REPL, web, gRPC) should depend only
// on this interface, not on *agentSession directly.
type Session interface {
	RunPrompt(ctx context.Context, prompt string) (string, bool, error)
	Compact()
	Steer(string)
	FollowUp(string)
	SetView(agent.View)
	SetCtrlCh(chan<- types.CtrlMsg)
	SetApproveFn(func(name, args string) bool)
	SetModel(providerName, modelName string)
	ProviderName() string
	ModelName() string
	MCPInfos() []mcp.ServerInfo
	Close()
}

// Compile-time check: agentSession satisfies Session.
var _ Session = (*agentSession)(nil)

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
	totalUsage types.Usage
}

func newAgentSession() (*agentSession, error) {
	// Migrate legacy ~/.yaah/mcp/*.json manifests into config.yaml.
	if n, err := config.MigrateMCP(); err == nil && n > 0 {
		fmt.Fprintf(os.Stderr, "%s migrated %d MCP server(s) from ~/.yaah/mcp/ to config.yaml\n", Dim("notice:"), n)
	}

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
	if err == nil && resumeSessionID == "" {
		systemPrompt += "\n\n## Memory Guidelines\n- Use memory_search to find relevant memories before answering personal/project questions. Pass a tag to filter by category.\n- When the user asks about past conversations or session history, use memory_search_sessions with an empty query to list recent transcripts.\n- Use memory_add to save important facts. Always include a tags array (e.g., [\"user_info\"], [\"preferences\"], [\"project:yaah\"], [\"decision\"]).\n- Use memory_update to correct stale facts (requires the memory ID). Use memory_delete to remove incorrect memories.\n- At the end of a conversation or when the user says goodbye, use memory_add to save a 2-3 line summary of key discussion points with tag [\"session_summary\"]."
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
		}
	}
	mcpClients, mcpTools, mcpInfos, mcpErr := mcp.StartMCPClientsFromConfig(context.Background(), mcpManifests, io.Discard)
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
		restored, err := db.GetSession(resumeSessionID)
		if err != nil {
			return nil, fmt.Errorf("cannot resume session %s: %w", resumeSessionID, err)
		}
		dbMsgs, err := db.GetMessages(resumeSessionID)
		if err != nil {
			return nil, fmt.Errorf("cannot resume session %s: %w", resumeSessionID, err)
		}
		if len(dbMsgs) == 0 {
			return nil, fmt.Errorf("session %s not found or has no messages", resumeSessionID)
		}
		messages = make([]types.Message, 0, len(dbMsgs)+1)
		if restored.SystemPrompt != "" {
			systemPrompt = restored.SystemPrompt
		}
		if restored.CompactedSummary != "" {
			messages = append(messages, types.SystemMsg(restored.CompactedSummary))
		}
		for _, m := range dbMsgs {
			msg := types.Message{
				Role:             m.Role,
				Content:          m.Content,
				ReasoningContent: m.ReasoningContent,
				Name:             m.ToolName,
				ToolCallID:       m.ToolCallID,
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
				ID:           sessionID,
				StartedAt:    time.Now().Unix(),
				CWD:          cwd,
				Model:        modelName,
				SystemPrompt: systemPrompt,
			})
		}
	}

	tracker := &tools.ConflictTracker{}
	subAgentProvider, subAgentModel := resolveSubAgent(cfg)
	subCW := providers.ResolveWindow(modelName, cfg.Agent.Default.ContextWindow) / 2
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
		s.db.EndSession(s.sessionID, time.Now().Unix(), s.totalUsage.PromptTokens, s.totalUsage.CompletionTokens)
		s.db.Close()
	}
	for _, c := range s.mcpClients {
		c.Close()
	}
}

func (s *agentSession) Close()   { s.close() }
func (s *agentSession) Compact() { s.compactContext() }
func (s *agentSession) ProviderName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providerName
}
func (s *agentSession) ModelName() string          { s.mu.RLock(); defer s.mu.RUnlock(); return s.modelName }
func (s *agentSession) MCPInfos() []mcp.ServerInfo { return s.mcpInfos }
func (s *agentSession) RunPrompt(ctx context.Context, prompt string) (string, bool, error) {
	return s.runPrompt(ctx, prompt)
}

func (s *agentSession) Steer(text string) {
	select {
	case s.steerCh <- text:
	default:
		s.sendCtrl(&types.CtrlStatus{Text: "steer queue full"})
	}
}

func (s *agentSession) FollowUp(text string) {
	select {
	case s.followupCh <- text:
	default:
		s.sendCtrl(&types.CtrlStatus{Text: "follow-up queue full"})
	}
}

func (s *agentSession) sendCtrl(msg types.CtrlMsg) {
	s.mu.RLock()
	ch := s.ctrlCh
	s.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
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

	if tt := s.toolReg.Get("todowrite"); tt != nil {
		if ttp, ok := tt.(*tools.TodoWriteTool); ok {
			ttp.OnWrite = func() {
				select {
				case ch <- &types.CtrlTodos{Items: ttp.Store.List()}:
				default:
				}
			}
		}
	}
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

func (s *agentSession) compactContext() {
	s.mu.RLock()
	ch := s.ctrlCh
	s.mu.RUnlock()

	msg := func(text string) {
		if ch != nil {
			select {
			case ch <- &types.CtrlStatus{Text: text}:
			default:
			}
		} else {
			fmt.Fprintf(os.Stderr, "  %s\n", Dim(text))
		}
	}

	window := s.cfg.Agent.Default.ContextWindow
	if window <= 0 {
		window = 128000
	}

	msgs := s.messages
	if len(msgs) <= 4 {
		msg("context is already small enough")
		return
	}

	totalChars := 0
	for _, m := range msgs {
		totalChars += len(m.Content) + len(m.ReasoningContent)
		for _, tc := range m.ToolCalls {
			totalChars += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	estTokens := totalChars / 4
	target := window * 4 / 5
	if estTokens <= target {
		msg(fmt.Sprintf("context: %d/%d tokens (%d%%)", estTokens, window, estTokens*100/window))
		return
	}

	msg(fmt.Sprintf("context: %d/%d tokens (%d%%) — compacting...", estTokens, window, estTokens*100/window))

	sysMsg := msgs[0]
	rest := msgs[1:]
	keepRecent := 6
	if len(rest) <= keepRecent {
		msg("not enough messages to compact")
		return
	}
	split := len(rest) - keepRecent

	if protect := s.cfg.Agent.Default.ReasoningProtect; protect > 0 {
		if adj := agent.ProtectReasoningTurns(msgs, 1+split, protect); adj < 1+split {
			split = adj - 1
			if split < 0 {
				split = 0
			}
		}
	}

	oldMsgs := rest[:split]
	keepMsgs := rest[split:]

	var sb strings.Builder
	sb.WriteString("Summarize the following conversation excerpt. Keep the structured format below.\n\n")
	sb.WriteString("## Goal\n## Completed Work\n## Active Work\n## Pending Tasks\n## Key Decisions\n## Files Modified\n\n---\nConversation excerpt:\n\n")
	for _, m := range oldMsgs {
		if m.Content != "" {
			sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
		}
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("[tool:%s] %s\n", tc.Function.Name, tc.Function.Arguments))
		}
	}

	compactModel := s.cfg.Agent.Default.SmallModel
	if compactModel == "" {
		compactModel = s.modelName
	}

	req := types.ChatRequest{
		Model:    compactModel,
		Messages: []types.Message{types.UserMsg(sb.String())},
	}

	resp, err := s.provider.Send(context.Background(), req)
	if err != nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		s.messages = append([]types.Message{sysMsg}, keepMsgs...)
		msg("compacted (trimmed)")
		return
	}

	summary := resp.Choices[0].Message.Content
	newMsgs := []types.Message{sysMsg}
	newMsgs = append(newMsgs, types.SystemMsg("Previous conversation summary:\n"+summary))
	newMsgs = append(newMsgs, keepMsgs...)
	s.messages = newMsgs

	newTokens := 0
	for _, m := range s.messages {
		newTokens += len(m.Content) / 4
	}
	msg(fmt.Sprintf("compacted: %d/%d tokens (%d%%)", newTokens, window, newTokens*100/window))
}

// terminalView implements agent.View for REPL terminal output.
// It owns the spinner lifecycle and records whether streaming occurred.
type terminalView struct {
	spin     *spinner.Spinner
	stopOnce sync.Once
	streamed bool
}

func newTerminalView() *terminalView {
	return &terminalView{spin: spinner.New(nil, "Thinking...")}
}

// start begins the thinking indicator. Must be called before RunPrompt.
func (v *terminalView) start() {
	fmt.Fprintln(os.Stderr)
	v.spin.Start()
}

func (v *terminalView) HandleEvent(evt agent.Event) {
	switch e := evt.(type) {
	case *agent.TokenDeltaEvent:
		v.stopOnce.Do(func() {
			v.spin.Stop()
			fmt.Fprintln(os.Stderr)
			v.streamed = true
		})
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

	case *agent.CompactionStartedEvent:
		// No-op in terminal mode — spinner already implies activity.

	case *agent.CompactionDoneEvent:
		// Brief, unobtrusive report in terminal mode.
		beforeK := float64(e.BeforeTokens) / 1000.0
		afterK := float64(e.AfterTokens) / 1000.0
		pct := e.SavingsPct * 100
		fmt.Fprintf(os.Stderr, "\n  %s %.0f%% (%.1fK → %.1fK, %s)\n",
			Dim("compacted"), pct, beforeK, afterK, Dim(e.Method))
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
	case *agent.DoneEvent:
		v.stopOnce.Do(v.spin.Stop)
		if v.streamed {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr)
		}
	}
}

// runPrompt executes a single agent prompt with the session's shared
// infrastructure. The caller must set a view via SetView before calling.
func (s *agentSession) runPrompt(ctx context.Context, prompt string) (string, bool, error) {
	compactProvider, compactModel := resolveCompact(s.cfg)
	fallbackProvider, fallbackModel := resolveFallback(s.cfg)

	s.mu.RLock()
	prov := s.provider
	mName := s.modelName
	v := s.view
	ctrl := s.ctrlCh
	appr := s.approveFn
	s.mu.RUnlock()

	if v == nil {
		v = agent.NoopView{}
	}

	loop := agent.NewLoop(prov, s.toolReg,
		agent.WithModel(mName),
		agent.WithSystemPrompt(s.systemPrompt),
		agent.WithView(v),
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
			ContextWindow:          providers.ResolveWindow(mName, s.cfg.Agent.Default.ContextWindow),
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

	response, err := loop.Run(ctx, prompt)

	s.messages = loop.Messages
	s.msgIdx = loop.Persister.MsgIdx()

	s.mu.Lock()
	s.totalUsage.PromptTokens += loop.TotalTokens.PromptTokens
	s.totalUsage.CompletionTokens += loop.TotalTokens.CompletionTokens
	s.totalUsage.TotalTokens += loop.TotalTokens.TotalTokens
	s.mu.Unlock()

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

	streamed := false
	if tv, ok := v.(*terminalView); ok {
		streamed = tv.streamed
	}

	return response, streamed, err
}
