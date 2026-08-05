// Package tui implements the terminal UI for yaah using bubbletea.
package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/banner"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/types"
)

// Message represents a chat message in the TUI.
type Message struct {
	Role         string
	Content      string // glamour-rendered for assistant, raw for others
	Raw          string // original markdown (for copy), same as Content for user/tool
	ToolName     string // tool that produced this message (for tool result messages)
	ToolArgs     string // tool arguments (for extracting descriptions)
	ToolDuration string // formatted duration string (e.g. "2.3s")
	Reasoning    string // thinking/reasoning text (empty for non-assistant or normal responses)
	SubRole      string // sub-agent role ("worker"/"reviewer"/"planner") for task tool messages
}

// ServerInfo holds status details about an MCP server (mirrors mcp.ServerInfo).
// Deprecated: use mcp.ServerInfo directly.
type ServerInfo = mcp.ServerInfo

// cursorHoverMsg is sent when the mouse moves over or leaves a clickable zone.
type cursorHoverMsg struct {
	hovering bool
}

// QuestionModal carries question data for the interactive modal dialog.
// Deprecated: use types.CtrlQuestion directly.
type QuestionModal = types.CtrlQuestion

// QuestionOption is a single choice in a question modal.
// Deprecated: use types.CtrlOption directly.
type QuestionOption = types.CtrlOption

// Command represents a slash command available in the TUI.
type Command struct {
	Name        string
	Description string
}

// defaultCommands lists the built-in colon commands (triggered by typing ":").
var defaultCommands = []Command{
	{Name: ":help", Description: "Show available commands"},
	{Name: ":clear", Description: "Clear chat history"},
	{Name: ":compact", Description: "Summarize old messages"},
	{Name: ":banner", Description: "Toggle ASCII art banner"},
	{Name: ":model", Description: "Switch model"},
	{Name: ":login", Description: "Authenticate with an OAuth provider"},
	{Name: ":logout", Description: "Remove stored OAuth credentials"},
	{Name: ":steer", Description: "Inject text into current turn before next provider call"},
	{Name: ":copyview", Description: "Copy rendered TUI view to clipboard"},
	{Name: ":quit", Description: "Exit the TUI"},
	{Name: ":stop", Description: "Abort the running agent"},
}

// Model is the bubbletea model for the yaah TUI.
// Model is the bubbletea model for the yaah TUI.
type Model struct {
	// --- core widgets ---
	messages   []Message
	viewport   viewport.Model
	input      textarea.Model
	spinner    spinner.Model
	help       help.Model
	mdRenderer *glamour.TermRenderer

	// --- static config ---
	banner        string // pre-rendered figlet + lolcat ASCII art
	provider      string
	modelName     string
	cwd           string
	contextWindow int
	version       string
	onSubmit      func(string)
	onQuit        func()
	onCompact     func()
	onModel       func(string, string)
	onFollowUp    func(string)
	onSteer       func(string)
	onAbort       func()
	onLogin       func()
	onLogout      func()

	// --- layout ---
	width  int
	height int

	// --- streaming response state ---
	thinking      bool
	toolCall      string
	toolArgs      string // args for current tool call (e.g. task description)
	streaming     bool   // currently streaming a response
	compacting    bool   // currently running context compaction
	streamContent string // accumulated streaming content
	thinkContent  string // accumulated thinking/reasoning content
	activePrompt  string // current prompt shown in info bar when active

	// --- reasoning ---
	reasoningExpanded map[string]bool // zone ID → true if expanded
	reasoningZones    []string        // active reasoning zone IDs
	toolExpanded      map[string]bool // zone ID → true for expanded tool output
	toolZones         []string        // active tool zone IDs

	// --- overlays ---
	showHelp   bool // help overlay visible
	searchMode bool // search overlay active

	// --- search ---
	searchQuery   string
	searchMatches []int
	searchIdx     int // current match index (-1 = none)

	// --- question modal ---
	questionMode  bool
	questionModal QuestionModal
	questionIdx   int
	questionMulti []bool
	// --- command mode ---
	commandMode bool
	commands    []Command

	// --- model selection ---
	modelMode     bool
	modelItems    []string
	modelSelected int
	providerNames map[string]string // provider key → display name

	// --- context window ---
	contextPct    int
	contextTokens int

	// --- mcp ---
	mcpInfos []ServerInfo

	// --- todos ---
	todos []todo.Item

	// --- cursor hover ---
	hoveredZone bool // true when mouse is over a clickable zone (pointer cursor)

	// --- misc UI ---
	showBanner   bool
	needsRefresh bool
	ephemMsg     string
	ephemTimer   int
	lastBody     string // last rendered body for :copyview
	recordView   bool   // set by FlushEvent, triggers RecordTUIView in View()
}

// Config holds the immutable setup parameters for a TUI model.
// Use New(cfg) instead of positional arguments.
type Config struct {
	Provider      string
	Model         string
	CWD           string
	ContextWindow int
	Version       string
	OnSubmit      func(string)
	OnQuit        func()
	OnCompact     func()
	OnModel       func(string, string)
	// OnFollowUp is invoked when the user submits text while the agent
	// is already running. The text is queued for the next iteration
	// rather than starting a new turn. May be nil.
	OnFollowUp func(string)
	// OnSteer is invoked when the user sends an immediate mid-turn
	// interrupt (e.g. Ctrl-T). Injects before the next provider call.
	// May be nil.
	OnSteer func(string)
	// OnAbort is invoked when the user requests to stop the running
	// agent (Esc while thinking, or :stop command). May be nil.
	OnAbort func()
	// OnLogin is invoked when the user runs :login. May be nil.
	OnLogin func()
	// OnLogout is invoked when the user runs :logout. May be nil.
	OnLogout func()
}

// New creates a new TUI model from a Config.
func New(cfg Config) *Model {
	input := textarea.New()
	input.Placeholder = "Type a message..."
	input.Focus()
	input.CharLimit = 0
	input.SetWidth(80)
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxHeight = 8
	// Enter submits, Shift+Enter inserts newline.
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter"))

	sp := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(spinnerStyle),
	)

	vp := viewport.New()

	bn, _ := banner.Generate()

	return &Model{
		input:             input,
		spinner:           sp,
		viewport:          vp,
		banner:            bn,
		reasoningExpanded: make(map[string]bool),
		toolExpanded:      make(map[string]bool),
		help:              help.New(),
		showBanner:        true,
		cwd:               cfg.CWD,
		provider:          cfg.Provider,
		modelName:         cfg.Model,
		contextWindow:     cfg.ContextWindow,
		version:           cfg.Version,
		onSubmit:          cfg.OnSubmit,
		onQuit:            cfg.OnQuit,
		onCompact:         cfg.OnCompact,
		onModel:           cfg.OnModel,
		onFollowUp:        cfg.OnFollowUp,
		onSteer:           cfg.OnSteer,
		onAbort:           cfg.OnAbort,
		onLogin:           cfg.OnLogin,
		onLogout:          cfg.OnLogout,
		commands:          defaultCommands,
	}
}

// AddAssistantMessage renders markdown through glamour and stores both
// the rendered output (Content) and the raw markdown (Raw).
func (m *Model) AddAssistantMessage(raw string) {
	m.messages = append(m.messages, Message{
		Role:    "assistant",
		Content: m.renderMarkdown(raw),
		Raw:     raw,
	})
	m.refreshViewport()
	m.scrollToBottom()
}

// AddAssistantMessageWithReasoning adds an assistant message with attached reasoning text.
func (m *Model) AddAssistantMessageWithReasoning(raw, reasoning string) {
	m.messages = append(m.messages, Message{
		Role:      "assistant",
		Content:   m.renderMarkdown(raw),
		Raw:       raw,
		Reasoning: reasoning,
	})
	m.refreshViewport()
	m.scrollToBottom()
}

// reRenderMessages re-renders all assistant messages through the current
// glamour renderer (used on window resize when word-wrap width changes).

// headerHeight returns the number of lines the header occupies.
// Delegates to Header.Height() for dynamic two-column measurement.
func (m *Model) headerHeight() int {
	return NewHeader(m.banner, m.provider, m.modelName, m.showBanner, m.width, m.mcpInfos, m.version).Height()
}

// inputAreaHeight returns the number of lines the input area occupies
// including its rounded border (1 content + 2 border = 3 for single-line).
func (m *Model) inputAreaHeight() int {
	return m.input.Height() + 2
}

// refreshViewport rebuilds the viewport content from the current message state.
func (m *Model) refreshViewport() {
	m.viewport.SetContent(m.renderMessages())
}

// scrollToBottom programmatically scrolls the viewport to the end.
func (m *Model) scrollToBottom() {
	m.viewport.GotoBottom()
}

// AddMessage adds a message to the chat history.
func (m *Model) AddMessage(role, content string) {
	m.messages = append(m.messages, Message{Role: role, Content: content, Raw: content})
	m.refreshViewport()
	m.scrollToBottom()
}

// AddToolResult adds a tool result message. For todowrite, it parses the
// tool args and renders the todo table inline so it is always visible
// regardless of CtrlTodos delivery timing.
func (m *Model) AddToolResult(toolName, content, toolArgs, duration string) {
	rendered := m.renderToolResult(toolName, content)

	if toolName == "todowrite" && toolArgs != "" {
		if items := parseTodosFromArgs(toolArgs); len(items) > 0 {
			todoTable := NewTodoTable(items, m.width-8)
			rendered = rendered + "\n\n" + todoTable.Render()
		}
	}

	m.messages = append(m.messages, Message{
		Role:         "tool",
		Content:      rendered,
		Raw:          content,
		ToolName:     toolName,
		ToolArgs:     toolArgs,
		ToolDuration: duration,
	})
	m.refreshViewport()
	m.scrollToBottom()
}

// SetEphemeral sets a transient status message that auto-clears after
// ~3 seconds. Use for feedback like "Compacted." or "Copied!".
func (m *Model) SetEphemeral(msg string) {
	m.ephemMsg = msg
	m.ephemTimer = 15 // ~3 seconds at ~200ms spinner tick rate
}

// RegisterCommand adds a slash command at runtime. Safe to call from
// any goroutine (e.g., when MCP tools register themselves).
func (m *Model) RegisterCommand(name, description string) {
	m.commands = append(m.commands, Command{Name: name, Description: description})
}

// SetMCPInfos stores MCP server status info and adds an :mcp command.
func (m *Model) SetMCPInfos(infos []ServerInfo) {
	m.mcpInfos = infos
}

// executeCommand executes a colon command and adds the result to messages.
// :quit is handled by the caller (returns tea.Quit from Update).
func (m *Model) executeCommand(input string) {
	cmd := strings.TrimSpace(input)
	switch cmd {
	case ":help":
		var b strings.Builder
		b.WriteString("Available commands:\n")
		for _, c := range m.commands {
			b.WriteString(fmt.Sprintf("  %s  %s\n", c.Name, c.Description))
		}
		m.AddMessage("system", b.String())
	case ":clear":
		m.messages = nil
		m.refreshViewport()
	case ":compact":
		if m.onCompact != nil {
			m.onCompact()
		}
	case ":banner":
		m.showBanner = !m.showBanner
		m.adjustViewport()
		m.refreshViewport()
		if m.showBanner {
			m.SetEphemeral("Banner shown.")
		} else {
			m.SetEphemeral("Banner hidden. Use /banner to show it again.")
		}
	case ":model":
		if len(m.modelItems) == 0 {
			m.AddMessage("system", "No models available. Configure providers or wait for model list to load.")
			return
		}
		m.modelMode = true
		m.modelSelected = 0
		m.input.SetValue("")
		m.input.Placeholder = "Search models..."
		m.clearCommandMode()
		m.adjustViewport()
		return
	case ":mcp":
		m.AddMessage("system", m.renderMCPStatus())
	case ":login":
		if m.onLogin != nil {
			m.onLogin()
		} else {
			m.AddMessage("system", "Login not available in this mode.")
		}
	case ":logout":
		if m.onLogout != nil {
			m.onLogout()
		} else {
			m.AddMessage("system", "Logout not available in this mode.")
		}
	case ":stop":
		if !m.thinking {
			m.AddMessage("system", "No agent is running.")
			return
		}
		if m.onAbort != nil {
			m.onAbort()
		}
		m.SetEphemeral("Agent stopped.")
	case ":copyview":
		scanned := zone.Scan(m.lastBody)
		plain := stripANSI(scanned)
		if err := clipboard.WriteAll(plain); err != nil {
			m.SetEphemeral("Copy failed: " + err.Error())
		} else {
			m.SetEphemeral("View copied to clipboard.")
		}
	default:
		// :steer is the only command that takes an argument, so it
		// doesn't fit cleanly into a static switch case. Match the
		// prefix and handle it before falling through to unknown.
		if strings.HasPrefix(cmd, ":steer") {
			body := strings.TrimSpace(strings.TrimPrefix(cmd, ":steer"))
			if body == "" {
				m.AddMessage("system", "Usage: :steer <text to inject>")
				return
			}
			if !m.thinking {
				m.AddMessage("system", "Steer is only meaningful while the agent is running. Type and press Enter to send a new message instead.")
				return
			}
			m.AddMessage("user", body+"  ⚡")
			if m.onSteer != nil {
				m.onSteer(body)
			}
			return
		}
		m.AddMessage("system", fmt.Sprintf("Unknown command: %s", cmd))
	}
}

// selectModel applies the currently highlighted model and exits model mode.
func (m *Model) selectModel() {
	filtered := m.filteredModels()
	if m.modelSelected < len(filtered) {
		selected := filtered[m.modelSelected]
		parts := strings.SplitN(selected, "/", 2)
		providerName := parts[0]
		modelName := selected
		if len(parts) == 2 {
			modelName = parts[1]
		}
		m.provider = providerName
		m.modelName = modelName
		if m.onModel != nil {
			m.onModel(providerName, modelName)
		}
	}
	m.exitModelMode()
}

// filteredModels returns modelItems filtered by the current input value.
func (m *Model) filteredModels() []string {
	filter := strings.ToLower(m.input.Value())
	if filter == "" {
		return m.modelItems
	}
	var out []string
	for _, model := range m.modelItems {
		if strings.Contains(strings.ToLower(model), filter) {
			out = append(out, model)
		}
	}
	return out
}

// exitModelMode exits model-selection mode and resets the input.
func (m *Model) exitModelMode() {
	m.modelMode = false
	m.modelSelected = 0
	m.input.SetValue("")
	m.input.Placeholder = "Type a message..."
	m.adjustViewport()
}

// SetThinking sets the thinking state.
func (m *Model) SetThinking(thinking bool) {
	m.thinking = thinking
	m.refreshViewport()
}

// SetCompacting sets the compaction state. When true, displays a
// compaction indicator in the status area.
func (m *Model) SetCompacting(compacting bool) {
	m.compacting = compacting
	m.refreshViewport()
}

// SetToolCall sets the current tool call display.
func (m *Model) SetToolCall(name, args string) {
	m.toolCall = name
	m.toolArgs = args
	m.refreshViewport()
}

// ClearToolCall clears the tool call display.
func (m *Model) ClearToolCall() {
	m.toolCall = ""
	m.toolArgs = ""
	m.refreshViewport()
}

// AppendToken appends a streaming token to the current response.
// To avoid excessive viewport rebuilds during fast streaming, only
// a full refresh + scroll runs when the debounce flag is cleared.
// The spinner tick (which fires ~15 times/sec) picks up pending
// refreshes.
func (m *Model) AppendToken(token string) {
	if !m.streaming {
		m.streaming = true
		m.streamContent = ""
		m.needsRefresh = true
	}

	m.streamContent += token

	// Refresh immediately if no pending refresh, then set the debounce flag.
	if !m.needsRefresh {
		m.refreshViewport()
		m.scrollToBottom()
		m.needsRefresh = true
	}
}

// HandleEvent processes typed agent events from the broker.
// Called from the bubbletea event loop via tea.Send in the forwarder goroutine.
func (m *Model) HandleEvent(evt agent.Event) {
	switch e := evt.(type) {
	case *agent.TokenDeltaEvent:
		m.AppendToken(e.Text)

	case *agent.ThinkingEvent:
		m.thinkContent += e.Text
		m.refreshViewport()
		m.scrollToBottom()

	case *agent.FlushEvent:
		haveReasoning := m.thinkContent != ""
		m.streaming = false
		m.streamContent = ""
		m.recordView = true
		if haveReasoning {
			reasoning := m.thinkContent
			m.thinkContent = ""
			m.AddAssistantMessageWithReasoning(e.Content, reasoning)
		} else {
			m.AddAssistantMessage(e.Content)
		}

	case *agent.ToolStartEvent:
		if m.thinkContent != "" {
			reasoning := m.thinkContent
			m.thinkContent = ""
			m.AddAssistantMessageWithReasoning("", reasoning)
		}
		m.SetToolCall(e.Name, e.Args)

	case *agent.ToolEndEvent:
		m.ClearToolCall()
		m.AddToolResult(e.Name, e.Result, e.Args, formatDuration(e.Duration))

	case *agent.CompactionStartedEvent:
		m.SetCompacting(true)

	case *agent.CompactionDoneEvent:
		m.SetCompacting(false)
		beforeK := float64(e.BeforeTokens) / 1000.0
		afterK := float64(e.AfterTokens) / 1000.0
		pct := e.SavingsPct * 100
		note := ""
		if e.IneffectiveNote != "" {
			note = " ⚠ " + e.IneffectiveNote
		}
		m.messages = append(m.messages, Message{
			Role: "compaction",
			Content: fmt.Sprintf("Compacted %.1fK → %.1fK tokens (%.0f%% savings, %s) in %.1fs%s  [old=%d keep=%d budget=%d]",
				beforeK, afterK, pct, e.Method, e.ElapsedSeconds, note,
				e.OldMsgCount, e.KeepMsgCount, e.Budget),
		})
		m.refreshViewport()
		m.scrollToBottom()

	case *agent.SubAgentStartEvent:
		displayName := subagent.RoleDisplayName(subagent.SubAgentRole(e.Role))
		specialty := subagent.RoleSpecialty(subagent.SubAgentRole(e.Role))
		label := displayName
		if specialty != "" {
			label += " - " + specialty
		}
		if e.Prompt != "" {
			label += " · " + e.Prompt
		}
		m.messages = append(m.messages, Message{
			Role:    "subagent-start",
			Content: label,
		})
		m.refreshViewport()
		m.scrollToBottom()

	case *agent.SubAgentEndEvent:
		displayName := subagent.RoleDisplayName(subagent.SubAgentRole(e.Role))
		specialty := subagent.RoleSpecialty(subagent.SubAgentRole(e.Role))
		label := displayName
		if specialty != "" {
			label += " - " + specialty
		}
		if e.Model != "" {
			label += " [" + e.Model + "]"
		}
		status := "completed"
		if e.Error != "" {
			status = e.Error
		}
		label += " · " + status
		if e.Duration > 0 {
			label += " (" + formatDuration(e.Duration) + ")"
		}
		m.messages = append(m.messages, Message{
			Role:    "subagent-end",
			Content: label,
		})
		m.refreshViewport()
		m.scrollToBottom()

	case *agent.EscalationEvent:
		severity := e.Severity
		prefix := "⚠"
		switch severity {
		case "blocker", "critical":
			prefix = "🛑"
		case "info":
			prefix = "ℹ"
		}
		content := prefix + " ESCALATION [" + severity + "] " + e.SubAgentRole + ": " + e.Summary
		if e.Detail != "" {
			content += "\n" + e.Detail
		}
		if e.Suggestion != "" {
			content += "\n→ " + e.Suggestion
		}
		m.messages = append(m.messages, Message{
			Role:    "escalation",
			Content: content,
		})
		m.refreshViewport()
		m.scrollToBottom()

	case *agent.DoneEvent:
		m.SetThinking(false)
		m.ClearToolCall()
		if e.Error != "" {
			m.AddMessage("error", e.Error)
		}
		haveReasoning := m.thinkContent != ""
		if m.streaming && m.streamContent != "" {
			content := m.streamContent
			m.streaming = false
			m.streamContent = ""
			if haveReasoning {
				reasoning := m.thinkContent
				m.thinkContent = ""
				m.AddAssistantMessageWithReasoning(content, reasoning)
			} else {
				m.AddAssistantMessage(content)
			}
		} else if e.Response != "" {
			if haveReasoning {
				reasoning := m.thinkContent
				m.thinkContent = ""
				m.AddAssistantMessageWithReasoning(e.Response, reasoning)
			} else {
				m.AddAssistantMessage(e.Response)
			}
		} else if haveReasoning {
			reasoning := m.thinkContent
			m.thinkContent = ""
			m.AddAssistantMessageWithReasoning("", reasoning)
		} else {
			m.thinkContent = ""
		}
		if e.ContextWindow > 0 {
			ct := e.ContextTokens
			if e.LastPromptTokens > 0 {
				ct = e.LastPromptTokens
			}
			m.HandleContextInfo(ct, e.ContextWindow)
		}

	default:
		// Unknown event — silently ignore for forward compatibility
	}
}

// handleControlMsg processes a control-plane message from the session.
func (m *Model) handleControlMsg(msg types.CtrlMsg) {
	switch ctrl := msg.(type) {
	case *types.CtrlStatus:
		m.AddMessage("system", ctrl.Text)
	case *types.CtrlTodos:
		m.todos = ctrl.Items
		m.refreshViewport()
	case *types.CtrlError:
		m.AddMessage("error", fmt.Sprintf("%v", ctrl.Err))
		m.SetThinking(false)
		m.streaming = false
		m.streamContent = ""
		m.activePrompt = ""
	case *types.CtrlQuestion:
		m.questionModal = QuestionModal{
			Header:   ctrl.Header,
			Question: ctrl.Question,
			Options:  make([]QuestionOption, len(ctrl.Options)),
			Multiple: ctrl.Multiple,
			AnswerCh: ctrl.AnswerCh,
		}
		for i, o := range ctrl.Options {
			m.questionModal.Options[i] = QuestionOption(o)
		}
		m.questionIdx = 0
		m.questionMulti = make([]bool, len(ctrl.Options))
		m.questionMode = true
		m.input.SetValue("")
		m.input.Placeholder = ""
		m.adjustViewport()
		m.refreshViewport()
	case *types.CtrlApproval:
		ch := make(chan string, 1)
		m.questionModal = QuestionModal{
			Header:   "Approve",
			Question: fmt.Sprintf("Run %s(%s)?", ctrl.Name, ctrl.Args),
			Options: []QuestionOption{
				{Label: "Yes", Description: "Approve this tool call"},
				{Label: "No", Description: "Deny this tool call"},
			},
			Multiple: false,
			AnswerCh: ch,
		}
		m.questionIdx = 0
		m.questionMulti = make([]bool, 2)
		m.questionMode = true
		m.input.SetValue("")
		m.input.Placeholder = ""
		m.adjustViewport()
		m.refreshViewport()
		go func() {
			answer := <-ch
			ctrl.ApproveCh <- (answer == "Yes" || answer == "Yes, Yes")
		}()
	case *types.CtrlContextInfo:
		ct := ctrl.Tokens
		if ctrl.LastPromptTokens > 0 {
			ct = ctrl.LastPromptTokens
		}
		m.HandleContextInfo(ct, ctrl.Window)
	case *types.CtrlFallback:
		m.provider = ctrl.Provider
		m.modelName = ctrl.Model
	case *types.CtrlModelList:
		m.modelItems = ctrl.Models
		if ctrl.ProviderNames != nil {
			m.providerNames = ctrl.ProviderNames
		}
		m.refreshViewport()
	case *types.CtrlDone:
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.InterruptMsg:
		if m.onAbort != nil {
			m.onAbort()
		}
		if m.onQuit != nil {
			m.onQuit()
		}
		return m, tea.Quit

	case agent.Event:
		m.HandleEvent(msg)
		return m, nil

	case types.CtrlMsg:
		m.handleControlMsg(msg)
		return m, nil

	case cursorHoverMsg:
		if m.hoveredZone != msg.hovering {
			m.hoveredZone = msg.hovering
		}
		return m, nil

	case spinner.TickMsg:
		return m, m.handleSpinnerTick(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(msg.Width - 4)
		m.createRenderer()
		m.reRenderMessages()
		m.adjustViewport()
		return m, nil

	case tea.MouseClickMsg:
		return m, m.handleMouseClick(msg)

	case tea.MouseMotionMsg:
		// Check if mouse is over a clickable zone (from previous render).
		for _, zoneID := range m.reasoningZones {
			if z := zone.Get(zoneID); z != nil && z.InBounds(msg) {
				if !m.hoveredZone {
					return m, func() tea.Msg { return cursorHoverMsg{hovering: true} }
				}
				return m, m.viewportUpdate(msg)
			}
		}
		for _, zoneID := range m.toolZones {
			if z := zone.Get(zoneID); z != nil && z.InBounds(msg) {
				if !m.hoveredZone {
					return m, func() tea.Msg { return cursorHoverMsg{hovering: true} }
				}
				return m, m.viewportUpdate(msg)
			}
		}
		if m.questionMode {
			for i := range m.questionModal.Options {
				zoneID := fmt.Sprintf("question-opt-%d", i)
				if z := zone.Get(zoneID); z != nil && z.InBounds(msg) {
					if !m.hoveredZone {
						return m, func() tea.Msg { return cursorHoverMsg{hovering: true} }
					}
					return m, m.viewportUpdate(msg)
				}
			}
		}
		if m.hoveredZone {
			return m, func() tea.Msg { return cursorHoverMsg{hovering: false} }
		}
		return m, m.viewportUpdate(msg)

	case tea.MouseMsg:
		// Forward mouse events to viewport (wheel scroll).
		return m, m.viewportUpdate(msg)

	case tea.KeyPressMsg:
		return m.handleKeyPress(msg)
	}

	var cmd tea.Cmd
	oldHeight := m.input.Height()
	m.input, cmd = m.input.Update(msg)
	if m.input.Height() != oldHeight {
		m.adjustViewport()
	}
	m.detectCommandMode()
	return m, cmd
}

// --- spinner ---

func (m *Model) handleSpinnerTick(msg spinner.TickMsg) tea.Cmd {
	var spinCmd tea.Cmd
	m.spinner, spinCmd = m.spinner.Update(msg)
	if m.thinking {
		m.refreshViewport()
	}
	if m.needsRefresh && m.streaming {
		m.refreshViewport()
		m.scrollToBottom()
		m.needsRefresh = false
	}
	if m.ephemTimer > 0 {
		m.ephemTimer--
		if m.ephemTimer == 0 {
			m.ephemMsg = ""
		}
	}
	return spinCmd
}

// --- mouse ---

func (m *Model) handleMouseClick(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button == tea.MouseLeft {
		if m.questionMode {
			for i := range m.questionModal.Options {
				zoneID := fmt.Sprintf("question-opt-%d", i)
				if z := zone.Get(zoneID); z != nil && z.InBounds(msg) {
					if m.questionModal.Multiple {
						m.questionMulti[i] = !m.questionMulti[i]
					}
					m.questionIdx = i
					m.refreshViewport()
					return nil
				}
			}
			return nil
		}
		for _, zoneID := range m.reasoningZones {
			if z := zone.Get(zoneID); z != nil && z.InBounds(msg) {
				m.reasoningExpanded[zoneID] = !m.reasoningExpanded[zoneID]
				m.refreshViewport()
				return nil
			}
		}
		for _, zoneID := range m.toolZones {
			if z := zone.Get(zoneID); z != nil && z.InBounds(msg) {
				m.toolExpanded[zoneID] = !m.toolExpanded[zoneID]
				m.refreshViewport()
				return nil
			}
		}
	}
	return m.viewportUpdate(msg)
}

func (m *Model) viewportUpdate(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return cmd
}

// --- key dispatch ---

// handleKeyPress routes a key press to the active mode handler.
func (m *Model) handleKeyPress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Dismiss overlays first
	if m.showHelp {
		m.showHelp = false
		m.adjustViewport()
		return m, nil
	}
	if m.searchMode {
		return m, m.handleSearchKey(msg)
	}
	if m.questionMode {
		return m, m.handleQuestionKey(msg)
	}
	if m.modelMode {
		return m, m.handleModelKey(msg)
	}
	return m, m.handleNormalKey(msg)
}

func (m *Model) handleSearchKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Cancel):
		m.searchMode = false
		m.searchQuery = ""
		return nil
	case key.Matches(msg, keys.NextMatch):
		m.searchNextMatch()
		return nil
	case key.Matches(msg, keys.PrevMatch):
		m.searchPrevMatch()
		return nil
	case key.Matches(msg, keys.Submit):
		m.searchMode = false
		return nil
	case msg.String() == "backspace":
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
			m.buildSearchMatches()
		}
		return nil
	default:
		s := msg.String()
		if len(s) == 1 && s[0] >= 32 && s[0] < 127 {
			m.searchQuery += s
			m.buildSearchMatches()
		}
		return nil
	}
}

func (m *Model) handleQuestionKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Cancel):
		m.answerQuestion("")
		return nil
	case key.Matches(msg, keys.Submit):
		m.commitQuestionAnswer()
		return nil
	case key.Matches(msg, keys.Up):
		if m.questionIdx > 0 {
			m.questionIdx--
		}
		m.refreshViewport()
		return nil
	case key.Matches(msg, keys.Down):
		if m.questionIdx < len(m.questionModal.Options)-1 {
			m.questionIdx++
		}
		m.refreshViewport()
		return nil
	case msg.String() == "space":
		if m.questionModal.Multiple {
			m.questionMulti[m.questionIdx] = !m.questionMulti[m.questionIdx]
		}
		m.refreshViewport()
		return nil
	}
	return nil
}

func (m *Model) handleModelKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Cancel):
		m.exitModelMode()
		return nil
	case key.Matches(msg, keys.Up):
		if m.modelSelected > 0 {
			m.modelSelected--
		}
		return nil
	case key.Matches(msg, keys.Down):
		filtered := m.filteredModels()
		if m.modelSelected < len(filtered)-1 {
			m.modelSelected++
		}
		return nil
	case key.Matches(msg, keys.Submit):
		m.selectModel()
		return nil
	}
	return nil
}

func (m *Model) handleNormalKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Quit):
		if m.onQuit != nil {
			m.onQuit()
		}
		return tea.Quit

	case key.Matches(msg, keys.Cancel):
		if m.thinking && m.onAbort != nil {
			m.onAbort()
		}
		if m.commandMode {
			m.input.SetValue("")
			m.clearCommandMode()
		}
		return nil

	case key.Matches(msg, keys.Help):
		if !m.commandMode && m.input.Value() == "" {
			m.showHelp = true
			m.adjustViewport()
			return nil
		}

	case key.Matches(msg, keys.Search):
		if !m.commandMode && m.input.Value() == "" {
			m.searchMode = true
			m.searchQuery = ""
			m.searchMatches = nil
			m.searchIdx = -1
			return nil
		}

	case key.Matches(msg, keys.Copy):
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == "assistant" && m.messages[i].Raw != "" {
				return tea.SetClipboard(m.messages[i].Raw)
			}
		}
		return nil

	case key.Matches(msg, keys.Reasoning):
		if m.hasReasoning() {
			anyExpanded := false
			for _, zid := range m.reasoningZones {
				if m.reasoningExpanded[zid] {
					anyExpanded = true
					break
				}
			}
			for _, zid := range m.reasoningZones {
				m.reasoningExpanded[zid] = !anyExpanded
			}
			m.refreshViewport()
		}
		return nil

	case key.Matches(msg, keys.Top):
		if !m.commandMode {
			m.viewport.GotoTop()
		}
		return nil

	case key.Matches(msg, keys.Bottom):
		if !m.commandMode {
			m.viewport.GotoBottom()
		}
		return nil

	case key.Matches(msg, keys.Submit):
		if m.commandMode {
			value := m.input.Value()
			m.input.SetValue("")
			m.clearCommandMode()
			if strings.TrimSpace(value) == ":quit" {
				return tea.Quit
			}
			m.executeCommand(value)
			return nil
		}
		value := m.input.Value()
		if strings.TrimSpace(value) == "" {
			return nil
		}
		if m.thinking {
			m.activePrompt = value
			m.input.SetValue("")
			if m.onFollowUp != nil {
				m.onFollowUp(value)
			}
			return nil
		}
		m.thinkContent = ""
		m.reasoningExpanded = make(map[string]bool)
		m.SetThinking(true)
		m.activePrompt = value
		m.input.SetValue("")
		if m.onSubmit != nil {
			m.onSubmit(value)
		}
		return nil

	case key.Matches(msg, keys.Up), key.Matches(msg, keys.Down),
		key.Matches(msg, keys.PageUp), key.Matches(msg, keys.PageDown):
		if !m.commandMode {
			return m.viewportUpdate(msg)
		}
	}

	// Key not consumed by any binding — forward to text input.
	var cmd tea.Cmd
	oldHeight := m.input.Height()
	m.input, cmd = m.input.Update(msg)
	if m.input.Height() != oldHeight {
		m.adjustViewport()
	}
	m.detectCommandMode()
	return cmd
}

// detectCommandMode enables or disables command mode based on the input prefix.
func (m *Model) detectCommandMode() {
	if m.modelMode || m.questionMode {
		return
	}
	if strings.HasPrefix(m.input.Value(), ":") {
		if !m.commandMode {
			m.commandMode = true
			m.adjustViewport()
		}
	} else {
		if m.commandMode {
			m.clearCommandMode()
		}
	}
}

func (m *Model) clearCommandMode() {
	m.commandMode = false
	m.adjustViewport()
}

func (m *Model) hasReasoning() bool {
	if m.thinkContent != "" {
		return true
	}
	for _, msg := range m.messages {
		if msg.Reasoning != "" {
			return true
		}
	}
	return false
}

func (m *Model) answerQuestion(labels string) {
	m.questionModal.AnswerCh <- labels
	m.questionMode = false
	m.questionModal = QuestionModal{}
	m.questionMulti = nil
	m.input.Placeholder = "Type a message..."
	m.adjustViewport()
}

func (m *Model) commitQuestionAnswer() {
	if m.questionModal.Multiple {
		var selected []string
		for i, toggled := range m.questionMulti {
			if toggled {
				selected = append(selected, m.questionModal.Options[i].Label)
			}
		}
		m.answerQuestion(strings.Join(selected, ", "))
	} else {
		m.answerQuestion(m.questionModal.Options[m.questionIdx].Label)
	}
}

// paletteLines returns the number of terminal rows the command palette
// occupies when visible. Includes the rounded border (2) and padding (2).
// maxModelLines returns the maximum number of model items that can fit
// in the terminal without pushing the input off-screen.
func (m *Model) maxModelLines() int {
	if m.height == 0 {
		return 10
	}
	inputHeight := m.inputAreaHeight()
	available := m.height - m.headerHeight() - 1 - inputHeight // 1: status line
	items := available - 4                                     // border (2) + padding (2)
	if items < 1 {
		items = 1
	}
	return items
}

func (m *Model) maxQuestionLines() int {
	n := m.maxModelLines() - 6 // 6 = header + blank + question + blank + blank + help
	if n < 1 {
		n = 1
	}
	return n
}

func (m *Model) paletteLines() int {
	if m.showHelp {
		// Help overlay: title + 4 groups with headers + footer + border/padding
		// Rough estimate: 22 content lines + 4 border/padding = 26.
		// Cap at 80% of available terminal height.
		available := m.height - m.headerHeight() - 1 - m.inputAreaHeight() // status, dynamic input area
		if available < 10 {
			return 10
		}
		max := available * 4 / 5
		helpLines := 26
		if helpLines > max {
			helpLines = max
		}
		return helpLines
	}
	if m.questionMode {
		optCount := len(m.questionModal.Options)
		max := m.maxQuestionLines()
		visible := optCount
		if visible > max {
			visible = max
		}
		lines := 10 + visible // 4 (border+padding) + 6 (header+question+help+blanks) + options
		if optCount > visible {
			lines++ // overflow indicator
		}
		return lines
	}
	if m.modelMode {
		filtered := m.filteredModels()
		if len(filtered) == 0 {
			return 4
		}
		// Count display rows: model items + provider heading rows
		rowCount := len(filtered)
		providers := make(map[string]bool)
		for _, model := range filtered {
			parts := strings.SplitN(model, "/", 2)
			providers[parts[0]] = true
		}
		rowCount += len(providers)
		truncated := rowCount > m.maxModelLines()
		if truncated {
			rowCount = m.maxModelLines()
		}
		lines := 4 + rowCount
		if truncated {
			lines++ // overflow indicator
		}
		return lines
	}
	if !m.commandMode {
		return 0
	}
	filter := strings.TrimPrefix(strings.TrimSpace(m.input.Value()), ":")
	filter = strings.ToLower(filter)
	count := 0
	for _, c := range m.commands {
		name := strings.TrimPrefix(c.Name, ":")
		if filter == "" || strings.HasPrefix(strings.ToLower(name), filter) {
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return 4 + count // border (2) + padding (2) + command lines
}

// --- search ---

// buildSearchMatches scans the rendered message content for lines containing
// the current search query (case-insensitive) and populates m.searchMatches
// with line indices.
func (m *Model) buildSearchMatches() {
	m.searchMatches = nil
	m.searchIdx = -1
	if m.searchQuery == "" {
		return
	}
	query := strings.ToLower(m.searchQuery)
	content := m.viewport.View()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), query) {
			m.searchMatches = append(m.searchMatches, i)
		}
	}
	if len(m.searchMatches) > 0 {
		m.searchIdx = 0
		m.scrollToMatch()
	}
}

// searchNextMatch advances to the next search match.
func (m *Model) searchNextMatch() {
	if len(m.searchMatches) == 0 {
		return
	}
	m.searchIdx++
	if m.searchIdx >= len(m.searchMatches) {
		m.searchIdx = 0
	}
	m.scrollToMatch()
}

// searchPrevMatch moves to the previous search match.
func (m *Model) searchPrevMatch() {
	if len(m.searchMatches) == 0 {
		return
	}
	m.searchIdx--
	if m.searchIdx < 0 {
		m.searchIdx = len(m.searchMatches) - 1
	}
	m.scrollToMatch()
}

// scrollToMatch scrolls the viewport to the current search match line.
func (m *Model) scrollToMatch() {
	if m.searchIdx < 0 || m.searchIdx >= len(m.searchMatches) {
		return
	}
	m.viewport.SetYOffset(m.searchMatches[m.searchIdx])
}

// --- layout ---

// adjustViewport recalculates and applies the viewport height based on
// current terminal dimensions, overlay state, and dynamic input height.
func (m *Model) adjustViewport() {
	if m.height == 0 {
		return
	}
	// Reserve space for header, status line, minimum chat area, and overlays.
	// Whatever is left is the maximum input height (including its border).
	overhead := m.headerHeight() + NewInfoBar("", "", 0).Height() + NewStatusBar("", 0, 0, false, 0).Height()
	minChat := 5
	if m.ephemMsg != "" {
		overhead++
	}
	paletteH := 0
	if m.commandMode || m.modelMode || m.questionMode || m.showHelp {
		paletteH = m.paletteLines()
	}
	searchH := 0
	if m.searchMode {
		searchH = 1
	}
	maxInputContent := m.height - overhead - minChat - paletteH - searchH - 2 // -2: border
	if maxInputContent < 1 {
		maxInputContent = 1
	}
	m.input.MaxHeight = maxInputContent

	// input area = content lines + top/bottom border (2 lines)
	inputHeight := m.inputAreaHeight()
	chatHeight := m.height - overhead - inputHeight - paletteH - searchH - 2 // -2: viewport border
	if chatHeight < minChat {
		chatHeight = minChat
	}
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(chatHeight)
	m.refreshViewport()
}

// renderMessages produces the full chat content (messages, streaming,
// thinking indicator, tool call) as a single string suitable for
// handing to the viewport. Width is m.width; callers must ensure
// m.width is set.

// View implements tea.Model.
func (m *Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("Initializing...")
	}

	// Minimum size check: if the terminal is too small, show a message.
	if m.width < 60 || m.height < 20 {
		msg := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("9")).
			Render(fmt.Sprintf(
				"Terminal too small — yaah needs at least 60×20 (current: %d×%d)",
				m.width, m.height))
		v := tea.NewView(zone.Scan(msg))
		v.AltScreen = true
		v.MouseMode = tea.MouseModeAllMotion
		return v
	}

	// Header: figlet banner + provider/model line (or compact if hidden)
	header := NewHeader(m.banner, m.provider, m.modelName, m.showBanner, m.width, m.mcpInfos, m.version).Render()

	activeView := ""
	if m.thinking || m.streaming {
		activeView = stripANSI(m.spinner.View())
	}

	// Info bar (between header and viewport) — shows active prompt
	infoBar := NewInfoBar(m.activePrompt, activeView, m.width).Render()

	// Status bar (1 line): message count + context bar only.
	status := NewStatusBar(m.cwd, len(m.messages), m.contextPct, m.contextWindow > 0, m.width).Render()

	// Ephemeral message line (shown only when active, auto-clears)
	var ephemLine string
	if m.ephemMsg != "" {
		ephemLine = noticeStyle.
			Width(m.width).
			Render(m.ephemMsg)
	}

	// Viewport holds the scrollable chat history
	viewportView := m.viewport.View()

	viewportView = "\n" + viewportView + "\n"

	// Search indicator line
	var searchLine string
	if m.searchMode {
		matchInfo := ""
		if len(m.searchMatches) > 0 && m.searchIdx >= 0 {
			matchInfo = fmt.Sprintf("  [%d/%d]", m.searchIdx+1, len(m.searchMatches))
		} else if m.searchQuery != "" && len(m.searchMatches) == 0 {
			matchInfo = "  [no matches]"
		}
		searchLine = commandDescStyle.Render(fmt.Sprintf("/%s%s", m.searchQuery, matchInfo))
	}

	// Palette (shown above input when in command, model, question, or help mode)
	var palette string
	if m.showHelp {
		palette = NewHelpOverlay(m.width).Render()
	} else if m.questionMode {
		palette = NewQuestionPalette(m.questionModal, m.questionIdx, m.questionMulti, m.maxQuestionLines(), m.width).Render()
	} else if m.modelMode {
		palette = NewModelPalette(m.filteredModels(), m.providerNames, m.modelSelected, m.provider+"/"+m.modelName, m.maxModelLines(), m.width).Render()
	} else if m.commandMode {
		palette = NewCommandPalette(m.commands, m.input.Value(), m.width).Render()
	}

	// Input (1 line)
	inputView := m.input.View()

	// Pink border around input area
	inputView = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(0, 1).
		Width(m.width).
		Render(inputView)

	elements := []string{header, infoBar, viewportView, status}
	if ephemLine != "" {
		elements = append(elements, ephemLine)
	}
	if palette != "" {
		elements = append(elements, palette)
	}
	if searchLine != "" {
		elements = append(elements, searchLine)
	}
	elements = append(elements, inputView)
	body := lipgloss.JoinVertical(lipgloss.Left, elements...)
	m.lastBody = body
	scanned := zone.Scan(body)
	if m.recordView {
		m.recordView = false
		observability.RecordTUIView(body, scanned)
	}

	v := tea.NewView(scanned)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	// OSC 22: change terminal cursor to pointer when over a clickable zone.
	// Supported by Kitty, WezTerm, foot, iTerm2, and others; ignored by terminals
	// that don't understand it.
	if m.hoveredZone {
		v.Content += "\x1b]22;pointer\x07"
	} else {
		v.Content += "\x1b]22;text\x07"
	}
	return v
}

// shortenCWD returns the current working directory with $HOME replaced
// by ~, truncated to maxLen if longer.
func shortenCWD(cwd string, maxLen int) string {
	home, _ := os.UserHomeDir()
	s := cwd
	if home != "" && strings.HasPrefix(s, home) {
		s = "~" + s[len(home):]
	}
	if len(s) > maxLen && maxLen > 3 {
		s = "..." + s[len(s)-(maxLen-3):]
	}
	return s
}

// contextBar returns a 10-segment bar showing fill percentage.
func contextBar(pct int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	segments := 10
	filled := (pct*segments + 50) / 100 // round to nearest
	if filled == 0 && pct > 0 {
		filled = 1 // show at least one segment for non-zero
	}
	if filled > segments {
		filled = segments
	}
	empty := segments - filled
	if filled >= 8 {
		return fmt.Sprintf("[%s%s %d%%]", strings.Repeat("█", filled), strings.Repeat("░", empty), pct)
	}
	if filled >= 5 {
		return fmt.Sprintf("[%s%s %d%%]", strings.Repeat("▓", filled), strings.Repeat("░", empty), pct)
	}
	return fmt.Sprintf("[%s%s %d%%]", strings.Repeat("█", filled), strings.Repeat("░", empty), pct)
}

// toolIndent wraps each line of content to fit within the given width.
func toolIndent(width int, content string) string {
	width = max(width, 20)

	lines := strings.Split(content, "\n")
	var result strings.Builder
	for i, line := range lines {
		if i > 0 {
			result.WriteString("\n")
		}
		result.WriteString(wrapText(line, width))
	}
	return result.String()
}

// HandleContextInfo updates the context window display.
func (m *Model) HandleContextInfo(tokens, window int) {
	m.contextTokens = tokens
	m.contextWindow = window
	if window > 0 {
		m.contextPct = tokens * 100 / window
		if m.contextPct > 100 {
			m.contextPct = 100
		}
	}
}

// formatDuration returns a human-readable duration string.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func parseTodosFromArgs(args string) []todo.Item {
	type rawTodo struct {
		Content  string `json:"content"`
		Status   string `json:"status"`
		Priority string `json:"priority"`
	}
	var params struct {
		Todos []rawTodo `json:"todos"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return nil
	}
	items := make([]todo.Item, len(params.Todos))
	for i, t := range params.Todos {
		if t.Priority == "" {
			t.Priority = "medium"
		}
		items[i] = todo.Item{
			ID:       fmt.Sprintf("td-%d", i+1),
			Content:  t.Content,
			Status:   t.Status,
			Priority: t.Priority,
		}
	}
	return items
}
