// Package tui implements the terminal UI for yaah using bubbletea.
package tui

import (
	"fmt"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/banner"
	"github.com/buchenberg/yaah/internal/mcp"
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
