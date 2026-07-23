package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
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
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/tui"
	"github.com/buchenberg/yaah/internal/types"
	zone "github.com/lrstanley/bubblezone/v2"
	"github.com/spf13/cobra"
)

// tuiCmd launches the TUI interface.
var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the terminal UI",
	Long:  `Launch the interactive terminal UI with rich chat display, streaming, and tool call visualization.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTUI()
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

// sessionModel holds the current provider and model for the TUI session.
// It is updated when the user switches models via /model.
type sessionModel struct {
	mu       sync.Mutex
	provider string
	model    string
}

func (s *sessionModel) get() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.provider, s.model
}

func (s *sessionModel) set(provider, model string) {
	s.mu.Lock()
	s.provider = provider
	s.model = model
	s.mu.Unlock()
}

// fetchAllModels gathers model IDs from all configured providers.
// If a provider has a models: override in config, those are used.
// Otherwise, ListModels is called against the provider's /v1/models endpoint.
// Results are returned in "provider/model" format, sorted.
func fetchAllModels(ctx context.Context, cfg *config.Config) []string {
	var all []string

	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := cfg.Providers[name]

		if len(p.Models) > 0 {
			for _, m := range p.Models {
				all = append(all, name+"/"+m)
			}
			continue
		}

		client := providers.NewOpenAIClient(p.BaseURL, p.APIKey, p.TimeoutSeconds)
		models, err := client.ListModels(ctx)
		if err != nil {
			log.Printf("fetch models from %s: %v", name, err)
			continue
		}
		for _, m := range models {
			all = append(all, name+"/"+m)
		}
	}

	return all
}

// providerFor returns a provider client for the given provider name.
func providerFor(cfg *config.Config, name string) agent.Provider {
	if p, ok := cfg.Providers[name]; ok {
		if prov, ok2 := makeProvider(p); ok2 {
			return prov
		}
	}
	return &noProviderStub{}
}

// runTUI starts the bubbletea TUI.
func runTUI() error {
	// Suppress stderr globally while the TUI is active. Anything written to
	// stderr (MCP warnings, tool prompts, etc.) would bleed through the
	// alt-screen and break the layout. We restore stderr on exit.
	origStderr := os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err == nil {
		os.Stderr = devNull
	}
	defer func() {
		os.Stderr = origStderr
		if devNull != nil {
			devNull.Close()
		}
	}()

	zone.NewGlobal()

	// Detect and apply the theme (respects NO_COLOR, YAah_THEME env var,
	// and terminal background).
	tui.ApplyTheme(tui.DetectTheme())

	cfg, err := config.Load()
	if err != nil || cfg == nil {
		cfg = &config.Config{
			Agent: config.AgentConfig{
				Default: config.Defaults{
					Model:         "deepseek/deepseek-v4-pro",
					SmallModel:    "deepseek/deepseek-v4-flash",
					MaxIterations: 50,
				},
			},
		}
	}
	providerName := "unknown"
	modelName := "unknown"
	if cfg != nil {
		providerName = resolveProviderName(cfg)
		modelName = resolveModel(cfg)
	}

	// Initialise OpenTelemetry if configured.
	var otelShutdown func(context.Context) error
	if cfg != nil && cfg.Observability.Otel.Enabled {
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
			return fmt.Errorf("otel: %w", err)
		}
		otelShutdown = sd
	}
	defer func() {
		if otelShutdown != nil {
			otelShutdown(context.Background())
		}
	}()

	// --- One-time setup: load instructions, memory, MCP tools ---
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

	agentCh := make(chan tui.AgentMsg, 256)

	// Todo store for the session. OnWrite pushes the full list to the
	// TUI so the todo panel stays current as the agent updates it.
	todoStore := todo.NewStore()
	toolReg.Register(&tools.TodoWriteTool{
		Store: todoStore,
		OnWrite: func() {
			agentCh <- tui.AgentMsg{Todos: todoStore.List()}
		},
	})

	db, err := memory.OpenDefault()
	var sessionID string
	var msgIdx int
	var persistedCount int

	skillDirs := skillSearchPaths()
	if discovered := skills.Discover(skillDirs); len(discovered) > 0 {
		layers.Skills = prompts.BuildSkillsIndex(discovered)
	}

	systemPrompt := prompts.Build(layers)
	if err == nil {
		if entries, memErr := db.ListMemory(50); memErr == nil && len(entries) > 0 {
			var memLines []string
			for _, entry := range entries {
				memLines = append(memLines, "- "+entry.Text)
			}
			systemPrompt += "\n\n## Memory\n" + strings.Join(memLines, "\n")
		}
		systemPrompt += "\n\n## Memory Guidelines\n- Use memory_search to find relevant memories before answering personal/project questions. Pass a tag to filter by category.\n- When the user asks about past conversations or session history, use memory_search_sessions with an empty query to list recent transcripts.\n- Use memory_add to save important facts. Always include a tags array (e.g., [\"user_info\"], [\"preferences\"], [\"project:yaah\"], [\"decision\"]).\n- Use memory_update to correct stale facts (requires the memory ID). Use memory_delete to remove incorrect memories.\n- At the end of a conversation or when the user says goodbye, use memory_add to save a 2-3 line summary of key discussion points with tag [\"session_summary\"]."

		toolReg.Register(&tools.MemorySearchTool{DB: db})
		toolReg.Register(&tools.MemoryAddTool{DB: db})
		toolReg.Register(&tools.MemoryDeleteTool{DB: db})
		toolReg.Register(&tools.MemoryUpdateTool{DB: db})
		toolReg.Register(&tools.MemorySessionSearchTool{DB: db})

		cwd, _ := os.Getwd()
		sessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
		db.CreateSession(memory.Session{
			ID:        sessionID,
			StartedAt: time.Now().Unix(),
			CWD:       cwd,
			Model:     modelName,
		})

		defer func() {
			db.EndSession(sessionID, time.Now().Unix())
			db.Close()
		}()
	}

	// Start MCP clients once for the entire TUI session.
	mcpDirs := mcpSearchPaths(config.HomeDir())
	mcpClients, mcpTools, mcpInfos, _ := mcp.StartMCPClientsWithStderr(context.Background(), mcpDirs, io.Discard)
	for _, c := range mcpClients {
		defer c.Close()
	}
	for _, t := range mcpTools {
		toolReg.Register(t)
	}

	// Convert MCP server info for the TUI.
	var tuiMCPInfos []tui.ServerInfo
	for _, info := range mcpInfos {
		tuiMCPInfos = append(tuiMCPInfos, tui.ServerInfo{
			Name:      info.Name,
			Transport: info.Transport,
			Command:   info.Command,
			URL:       info.URL,
			Connected: info.Connected,
			ToolCount: info.ToolCount,
			Error:     info.Error,
		})
	}

	// Register the skill-loading tool
	toolReg.Register(&tools.SkillTool{Dirs: skillDirs})

	planDirs := planSearchPaths()
	toolReg.Register(&tools.PlanTool{Dirs: planDirs})

	procMgr := processpkg.NewManager()
	toolReg.Register(&tools.BackgroundProcessTool{Manager: procMgr})

	conflictTracker := &tools.ConflictTracker{}
	subAgentProvider, subAgentModel := resolveSubAgent(cfg)
	subCW := cfg.Agent.Default.ContextWindow / 2
	if subCW < 32000 {
		subCW = 32000
	}
	toolReg.Register(newTaskTool(resolveProvider(cfg), systemPrompt, modelName, db, sessionID, subAgentProvider, subAgentModel, cfg.Agent.SubAgent, reg.Names(), cfg.Observability.Otel.Enabled, cfg.Observability.Otel.Verbose, conflictTracker, cfg.Agent.Default.EstimateFactor, subCW, cfg.Agent.SubAgent.OutputLimit, cfg.Providers))

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

	// Shared conversation history for the TUI session.
	var messages []types.Message

	// Shared mutable state for the current provider/model.
	sm := &sessionModel{provider: providerName, model: resolveModel(cfg)}

	m := tui.New(tui.Config{
		Provider:      providerName,
		Model:         modelName,
		CWD:           cwd,
		ContextWindow: cfg.Agent.Default.ContextWindow,
		OnSubmit: func(input string) {
			pName, mName := sm.get()

			go runAgentForTUI(input, agentCh, cfg, systemPrompt, mName, toolReg, &messages, db, sessionID, &msgIdx, &persistedCount, sm, conflictTracker)
			_ = pName
		},
		OnQuit: func() {},
		OnCompact: func() {
			go func() {
				window := cfg.Agent.Default.ContextWindow
				if window <= 0 {
					window = 128000
				}
				if len(messages) <= 4 {
					agentCh <- tui.AgentMsg{Flush: "Context is already small enough."}
					return
				}
				totalChars := 0
				for _, m := range messages {
					totalChars += len(m.Content)
					for _, tc := range m.ToolCalls {
						totalChars += len(tc.Function.Arguments) + len(tc.Function.Name)
					}
				}
				if totalChars/4 <= window*4/5 {
					agentCh <- tui.AgentMsg{Flush: fmt.Sprintf("Context is already compact enough (%d/%d tokens).", totalChars/4, window)}
					return
				}

				sysMsg := messages[0]
				rest := messages[1:]
				keepRecent := 6
				if len(rest) <= keepRecent {
					agentCh <- tui.AgentMsg{Flush: "Not enough messages to compact."}
					return
				}
				split := len(rest) - keepRecent
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

				pName, mName := sm.get()
				compactProv := providerFor(cfg, pName)
				compactModel := cfg.Agent.Default.SmallModel
				if compactModel == "" {
					compactModel = mName
				}
				req := types.ChatRequest{
					Model:    compactModel,
					Messages: []types.Message{types.UserMsg(sb.String())},
				}
				resp, err := compactProv.Send(context.Background(), req)
				if err != nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
					// Fallback: trim oldest half
					messages = append([]types.Message{sysMsg}, keepMsgs...)
					agentCh <- tui.AgentMsg{Flush: "Compacted (trimmed)."}
					return
				}
				summary := resp.Choices[0].Message.Content
				newMsgs := []types.Message{sysMsg}
				newMsgs = append(newMsgs, types.SystemMsg("Previous conversation summary:\n"+summary))
				newMsgs = append(newMsgs, keepMsgs...)
				messages = newMsgs
				agentCh <- tui.AgentMsg{Flush: "Compacted."}
			}()
		},
		OnModel: func(pName, mName string) {
			sm.set(pName, mName)
		},
	})

	// Show MCP server status.
	m.SetMCPInfos(tuiMCPInfos)
	m.RegisterCommand(":mcp", "Show MCP server status")
	// Add an initial system message showing MCP status.
	if len(tuiMCPInfos) > 0 {
		var connected, failed int
		for _, info := range tuiMCPInfos {
			if info.Connected {
				connected++
			} else {
				failed++
			}
		}
		statusMsg := fmt.Sprintf("MCP: %d server", len(tuiMCPInfos))
		if len(tuiMCPInfos) > 1 {
			statusMsg += "s"
		}
		if connected > 0 {
			statusMsg += fmt.Sprintf(" (%d connected", connected)
			if failed > 0 {
				statusMsg += fmt.Sprintf(", %d failed", failed)
			}
			statusMsg += ")"
		} else {
			statusMsg += " — all failed"
		}
		statusMsg += ". Type :mcp for details."
		m.AddMessage("system", statusMsg)
	}

	// Panic recovery: catch panics in the main goroutine so the terminal
	// is restored. Note: panics in agent goroutines are not caught here.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(origStderr, "yaah panic: %v\n", r)
			os.Exit(1)
		}
	}()

	// Install suspend/resume signal handlers (no-op on Windows).
	stopSignals := installSignalHandlers()
	defer stopSignals()

	p := tea.NewProgram(m)

	// Wire the question tool handler for TUI modal dialogs.
	if qt := toolReg.Get("question"); qt != nil {
		if qtp, ok := qt.(*tools.QuestionTool); ok {
			qtp.Handler = func(entries []tools.QuestionEntry) []string {
				var answers []string
				for _, e := range entries {
					ch := make(chan string, 1)
					opts := make([]tui.QuestionOption, len(e.Options))
					for i, o := range e.Options {
						opts[i] = tui.QuestionOption{Label: o.Label, Description: o.Description}
					}
					p.Send(tui.AgentMsg{Question: &tui.QuestionModal{
						Header:   e.Header,
						Question: e.Question,
						Options:  opts,
						Multiple: e.Multiple,
						AnswerCh: ch,
					}})
					answer := <-ch
					answers = append(answers, fmt.Sprintf("%s: %s", e.Header, answer))
				}
				return answers
			}
		}
	}

	go func() {
		for msg := range agentCh {
			p.Send(msg)
		}
	}()

	// Pre-fetch model lists from all providers in the background.
	go func() {
		names := make(map[string]string)
		for key, p := range cfg.Providers {
			if p.Name != "" {
				names[key] = p.Name
			}
		}
		models := fetchAllModels(context.Background(), cfg)
		agentCh <- tui.AgentMsg{ModelList: models, ProviderNames: names}
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

// runAgentForTUI runs the agent loop for a single prompt and sends messages
// to the TUI channel. The channel is NOT closed here — it is shared across
// multiple prompts for the lifetime of the TUI session.
func runAgentForTUI(prompt string, ch chan<- tui.AgentMsg, cfg *config.Config, systemPrompt, modelName string, toolReg *tools.Registry, messages *[]types.Message, db *memory.DB, sessionID string, msgIdx *int, persistedCount *int, sm *sessionModel, conflictTracker *tools.ConflictTracker) {
	pName, _ := sm.get()
	provider := providerFor(cfg, pName)

	if cfg.Observability.Otel.Enabled {
		if sp, ok := provider.(agent.StreamProvider); ok {
			provider = &observability.InstrumentedProvider{Inner: sp, Verbose: cfg.Observability.Otel.Verbose}
		}
	}

	compactProvider, compactModel := resolveCompact(cfg)

	loop := &agent.Loop{
		Provider:              provider,
		Registry:              toolReg,
		Model:                 modelName,
		SystemPrompt:          systemPrompt,
		MaxInlineToolsPerTurn: cfg.Agent.Default.MaxInlineToolsPerTurn,
		MaxIterations:         cfg.Agent.Default.MaxIterations,
		ContextWindow:         cfg.Agent.Default.ContextWindow,
		EstimateFactor:        cfg.Agent.Default.EstimateFactor,
		ApprovalMode:          resolveApproval(cfg),
		Messages:              *messages,
		OtelEnabled:           cfg.Observability.Otel.Enabled,
		OtelVerbose:           cfg.Observability.Otel.Verbose,
		ConflictTracker:       conflictTracker,
		ToolsLevel:            agent.FullTools,
		CompactProvider:       compactProvider,
		CompactModel:          compactModel,
		ApproveFn: func(name, args string) bool {
			respCh := make(chan bool, 1)
			ch <- tui.AgentMsg{
				ApproveChan: respCh,
				ApproveName: name,
				ApproveArgs: args,
			}
			return <-respCh
		},
		OnToken: func(token string) {
			ch <- tui.AgentMsg{Token: token}
		},
		OnFlush: func(content string) {
			ch <- tui.AgentMsg{Flush: content}
		},
		OnTool: func(info agent.ToolInfo) {
			if info.Duration == 0 {
				ch <- tui.AgentMsg{ToolName: info.Name, ToolArgs: info.Args}
			} else {
				ch <- tui.AgentMsg{
					ToolResult:     info.Result,
					ToolResultName: info.Name,
					ToolArgs:       info.Args,
					ToolDuration:   formatDuration(info.Duration),
				}
			}
		},
		OnSubAgent: func(info agent.SubAgentInfo) {
			if info.Duration == 0 {
				ch <- tui.AgentMsg{
					SubAgentStart: true,
					SubAgentRole:  info.Role,
					SubAgentLabel: info.Prompt,
				}
			} else {
				dur := formatDuration(info.Duration)
				errStr := ""
				if info.Error != "" {
					errStr = info.Error
				}
				ch <- tui.AgentMsg{
					SubAgentEnd:   true,
					SubAgentRole:  info.Role,
					SubAgentModel: info.Model,
					SubAgentDur:   dur,
					SubAgentErr:   errStr,
				}
			}
		},
		OnThinking: func(text string) {
			ch <- tui.AgentMsg{Thinking: text}
		},
	}

	response, err := loop.Run(context.Background(), prompt)
	if err != nil {
		ch <- tui.AgentMsg{
			Err:           err,
			ContextTokens: loop.EstimatedTokens(),
			ContextWindow: loop.ContextWindow,
		}
		*messages = loop.Messages
		return
	}

	*messages = loop.Messages
	persistTUIMessages(db, sessionID, msgIdx, persistedCount, loop.Messages)
	ch <- tui.AgentMsg{
		Done:          true,
		Response:      response,
		ContextTokens: loop.EstimatedTokens(),
		ContextWindow: loop.ContextWindow,
	}
}

func persistTUIMessages(db *memory.DB, sessionID string, msgIdx *int, persistedCount *int, messages []types.Message) {
	if db == nil {
		return
	}
	newMsgs := messages[*persistedCount:]
	for _, m := range newMsgs {
		content := m.Content
		if content == "" {
			var parts []string
			for _, tc := range m.ToolCalls {
				parts = append(parts, fmt.Sprintf("[tool:%s] %s", tc.Function.Name, tc.Function.Arguments))
			}
			content = strings.Join(parts, "\n")
		}
		toolCallsJSON := ""
		if len(m.ToolCalls) > 0 {
			data, _ := json.Marshal(m.ToolCalls)
			toolCallsJSON = string(data)
		}
		toolName := ""
		if m.Role == "tool" {
			toolName = m.Name
		}
		msg := memory.Message{
			SessionID: sessionID,
			Idx:       *msgIdx,
			Role:      m.Role,
			Content:   content,
			ToolName:  toolName,
			ToolCalls: toolCallsJSON,
			Timestamp: time.Now().Unix(),
		}
		db.AddMessage(msg)
		*msgIdx++
	}
	*persistedCount = len(messages)
}
