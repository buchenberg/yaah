// Package tui implements the terminal UI for yaah using bubbletea.
package tui

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/buchenberg/yaah/internal/banner"
)

// Styles — declared here, initialized by ApplyTheme in theme.go.
var (
	titleStyle          lipgloss.Style
	userStyle           lipgloss.Style
	userBgStyle         lipgloss.Style
	assistantStyle      lipgloss.Style
	toolStyle           lipgloss.Style
	systemStyle         lipgloss.Style
	systemBgStyle       lipgloss.Style
	statusStyle         lipgloss.Style
	spinnerStyle        lipgloss.Style
	codeStyle           lipgloss.Style
	boldStyle           lipgloss.Style
	italicStyle         lipgloss.Style
	thinkingStyle       lipgloss.Style
	reasoningBgStyle    lipgloss.Style
	toggleStyle         lipgloss.Style
	listBulletStyle     lipgloss.Style
	listItemStyle       lipgloss.Style
	treeStyle           lipgloss.Style
	treeItemStyle       lipgloss.Style
	commandPaletteStyle lipgloss.Style
	commandNameStyle    lipgloss.Style
	commandDescStyle    lipgloss.Style
)

// Message represents a chat message in the TUI.
type Message struct {
	Role      string
	Content   string // glamour-rendered for assistant, raw for others
	Raw       string // original markdown (for copy), same as Content for user/tool
	ToolName  string // tool that produced this message (for tool result messages)
	ToolArgs  string // tool arguments (for extracting descriptions)
	Reasoning string // thinking/reasoning text (empty for non-assistant or normal responses)
}

// AgentMsg is a message from the agent goroutine.
type AgentMsg struct {
	Token          string
	Thinking       string // reasoning content from models like DeepSeek
	ToolName       string
	ToolArgs       string // tool arguments (for display, e.g. task description)
	ToolResult     string // tool result content
	ToolResultName string // tool name for the result
	Flush          string // streamed content to commit before a tool call
	Done           bool
	Response       string
	Err            error
	ContextTokens  int               // estimated tokens used, for status bar
	ContextWindow  int               // context window size, for status bar
	ModelList      []string          // models fetched from providers
	ProviderNames  map[string]string // provider key → display name
	Question       *QuestionModal    // non-nil when a question should be shown
	ApproveChan    chan bool         // set when asking for tool approval; the TUI sends true/false
	ApproveName    string            // tool name for approval display
	ApproveArgs    string            // abbreviated tool args for approval display
	MCPInfos       []ServerInfo      // MCP server status info (sent at startup)
}

// ServerInfo holds status details about an MCP server (mirrors mcp.ServerInfo).
type ServerInfo struct {
	Name      string
	Transport string
	Command   string
	URL       string
	Connected bool
	ToolCount int
	Error     string
}

// QuestionModal carries question data for the interactive modal dialog.
type QuestionModal struct {
	Header   string
	Question string
	Options  []QuestionOption
	Multiple bool
	AnswerCh chan<- string
}

// QuestionOption is a single choice in a question modal.
type QuestionOption struct {
	Label       string
	Description string
}

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
	{Name: ":quit", Description: "Exit the TUI"},
}

// Model is the bubbletea model for the yaah TUI.
// Model is the bubbletea model for the yaah TUI.
type Model struct {
	// --- core widgets ---
	messages   []Message
	viewport   viewport.Model
	input      textinput.Model
	spinner    spinner.Model
	help       help.Model
	mdRenderer *glamour.TermRenderer

	// --- static config ---
	banner        string // pre-rendered figlet + lolcat ASCII art
	provider      string
	modelName     string
	cwd           string
	contextWindow int
	onSubmit      func(string)
	onQuit        func()
	onCompact     func()
	onModel       func(string, string)

	// --- layout ---
	width  int
	height int

	// --- streaming response state ---
	thinking      bool
	toolCall      string
	toolArgs      string // args for current tool call (e.g. task description)
	streaming     bool   // currently streaming a response
	streamContent string // accumulated streaming content
	thinkContent  string // accumulated thinking/reasoning content

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
	approveModal  AgentMsg // pending approval request (ApproveChan set)

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

	// --- misc UI ---
	showBanner   bool
	needsRefresh bool
	ephemMsg     string
	ephemTimer   int
}

// Config holds the immutable setup parameters for a TUI model.
// Use New(cfg) instead of positional arguments.
type Config struct {
	Provider      string
	Model         string
	CWD           string
	ContextWindow int
	OnSubmit      func(string)
	OnQuit        func()
	OnCompact     func()
	OnModel       func(string, string)
}

// New creates a new TUI model from a Config.
func New(cfg Config) *Model {
	input := textinput.New()
	input.Placeholder = "Type a message..."
	input.Focus()
	input.CharLimit = 0
	input.SetWidth(80)

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
		onSubmit:          cfg.OnSubmit,
		onQuit:            cfg.OnQuit,
		onCompact:         cfg.OnCompact,
		onModel:           cfg.OnModel,
		commands:          defaultCommands,
	}
}

var (
	mdLinkRe   = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	autoLinkRe = regexp.MustCompile(`<((?:https?|ftp)://[^>]+)>`)
)

// osc8Link wraps text in an OSC 8 hyperlink for clickable terminal links.
func osc8Link(text, url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// injectHyperlinks converts markdown links, autolinks, and bare URLs into
// OSC 8 hyperlink sequences before glamour renders them.
func injectHyperlinks(md string) string {
	md = mdLinkRe.ReplaceAllStringFunc(md, func(match string) string {
		parts := mdLinkRe.FindStringSubmatch(match)
		if len(parts) == 3 {
			return osc8Link(parts[1], parts[2])
		}
		return match
	})
	md = autoLinkRe.ReplaceAllStringFunc(md, func(match string) string {
		url := strings.Trim(match, "<>")
		return osc8Link(url, url)
	})
	return md
}

type textSegment struct {
	content string
	isTable bool
}

// splitRow splits a pipe-delimited table row into trimmed columns.
func splitRow(line string) []string {
	line = strings.Trim(line, "| \t")
	cols := strings.Split(line, "|")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	return cols
}

func replacePattern(s, open, close string, style func(string) string) string {
	for {
		start := strings.Index(s, open)
		if start == -1 {
			break
		}
		end := strings.Index(s[start+len(open):], close)
		if end == -1 {
			break
		}
		end += start + len(open)
		inner := s[start+len(open) : end]
		styled := style(inner)
		s = s[:start] + styled + s[end+len(close):]
	}
	return s
}

func isWideRune(r rune) bool {
	return r >= 0x1100 && r <= 0x115F ||
		r >= 0x2E80 && r <= 0xA4CF ||
		r >= 0xAC00 && r <= 0xD7A3 ||
		r >= 0xF900 && r <= 0xFAFF ||
		r >= 0xFE30 && r <= 0xFE6F ||
		r >= 0xFF01 && r <= 0xFF60 ||
		r >= 0xFFE0 && r <= 0xFFE6 ||
		r >= 0x1B000 && r <= 0x1B2FF ||
		r >= 0x1F004 && r <= 0x1F251 ||
		r >= 0x20000 && r <= 0x3FFFD
}

// --- list and tree rendering ---

// bulletPattern matches markdown bullet list items (* item, - item, + item).
var bulletPattern = regexp.MustCompile(`(?m)^[*\-+]\s`)

// isListContent detects if content contains bullet list items.
func isListContent(s string) bool {
	return strings.Contains(s, "\n") && bulletPattern.MatchString(s)
}

var treeLineRe = regexp.MustCompile(`[├└]──`)

// isTreeContent detects tree-like content with box-drawing characters.
func isTreeContent(s string) bool {
	return treeLineRe.MatchString(s)
}

// splitTreePrefix separates tree-drawing characters from the node name.
func splitTreePrefix(line string) (prefix, name string) {
	treeChars := map[rune]bool{'│': true, ' ': true, '├': true, '└': true, '─': true, '┬': true}
	runes := []rune(line)
	i := 0
	for i < len(runes) {
		r := runes[i]
		if !treeChars[r] {
			if r == '\\' && i+1 < len(runes) {
				i += 2
				continue
			}
			break
		}
		i++
	}
	return string(runes[:i]), strings.TrimSpace(string(runes[i:]))
}

// treeDepth computes the depth from tree-drawing prefix characters.
func treeDepth(prefix string) int {
	depth := 0
	for _, r := range prefix {
		if r == '│' || r == '├' || r == '└' {
			depth++
		}
	}
	return depth
}

// displayWidth returns the approximate terminal display width of a string.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r <= 0x7F {
			w++
		} else if isWideRune(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
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

// headerHeight returns the number of lines the banner + provider header
// occupies. Used to size the viewport. When the banner is hidden, only the
// provider line counts.
func (m *Model) headerHeight() int {
	if !m.showBanner || m.banner == "" {
		return 2 // provider line + blank line
	}
	header := m.banner + "\n\n" +
		titleStyle.Render(fmt.Sprintf("%s/%s", m.provider, m.modelName)) + "\n"
	return len(strings.Split(header, "\n"))
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

// AddToolResult adds a tool result message. For todowrite, it renders the
// formatted todo list. For other tools, it shows the raw result.
func (m *Model) AddToolResult(toolName, content, toolArgs string) {
	m.messages = append(m.messages, Message{
		Role:     "tool",
		Content:  m.renderToolResult(toolName, content),
		Raw:      content,
		ToolName: toolName,
		ToolArgs: toolArgs,
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
	default:
		m.AddMessage("system", fmt.Sprintf("Unknown command: %s", cmd))
	}
}

// updateCommandSuggestions updates the textinput suggestions for command mode.
func (m *Model) updateCommandSuggestions() {
	var names []string
	for _, c := range m.commands {
		names = append(names, c.Name)
	}
	m.input.SetSuggestions(names)
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

// HandleAgentMsg processes messages from the agent goroutine.
func (m *Model) HandleAgentMsg(msg AgentMsg) {
	if msg.Err != nil {
		m.AddMessage("assistant", fmt.Sprintf("Error: %v", msg.Err))
		m.SetThinking(false)
		m.streaming = false
		m.streamContent = ""
		return
	}

	if msg.Flush != "" {
		haveReasoning := m.thinkContent != ""
		m.streaming = false
		m.streamContent = ""
		if haveReasoning {
			reasoning := m.thinkContent
			m.thinkContent = ""
			m.AddAssistantMessageWithReasoning(msg.Flush, reasoning)
		} else {
			m.AddAssistantMessage(msg.Flush)
		}
		return
	}

	if msg.Token != "" {
		m.AppendToken(msg.Token)
		return
	}

	if msg.Thinking != "" {
		m.thinkContent += msg.Thinking
		m.refreshViewport()
		m.scrollToBottom()
		return
	}

	if msg.ToolName != "" {
		if m.thinkContent != "" {
			reasoning := m.thinkContent
			m.thinkContent = ""
			m.AddAssistantMessageWithReasoning("", reasoning)
		}
		m.SetToolCall(msg.ToolName, msg.ToolArgs)
		return
	}

	if msg.ToolResult != "" || msg.ToolResultName != "" {
		m.ClearToolCall() // tool finished — collapse progress label
		m.AddToolResult(msg.ToolResultName, msg.ToolResult, msg.ToolArgs)
		return
	}

	if msg.Done {
		m.SetThinking(false)
		m.ClearToolCall()
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
		} else if msg.Response != "" {
			if haveReasoning {
				reasoning := m.thinkContent
				m.thinkContent = ""
				m.AddAssistantMessageWithReasoning(msg.Response, reasoning)
			} else {
				m.AddAssistantMessage(msg.Response)
			}
		} else {
			m.thinkContent = ""
		}
	}

	if msg.Question != nil {
		m.questionModal = *msg.Question
		m.questionIdx = 0
		m.questionMulti = make([]bool, len(msg.Question.Options))
		m.questionMode = true
		m.input.SetValue("")
		m.input.Placeholder = ""
		m.adjustViewport()
		m.refreshViewport()
		return
	}

	if msg.ApproveChan != nil {
		m.approveModal = msg
		ch := make(chan string, 1)
		m.questionModal = QuestionModal{
			Header:   "Approve",
			Question: fmt.Sprintf("Run %s(%s)?", msg.ApproveName, msg.ApproveArgs),
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
		// Answer in the background so we don't block the event loop.
		go func() {
			answer := <-ch
			msg.ApproveChan <- (answer == "Yes" || answer == "Yes, Yes")
		}()
		return
	}

	if msg.ContextWindow > 0 {
		m.HandleContextInfo(msg.ContextTokens, msg.ContextWindow)
	}

	if len(msg.ModelList) > 0 {
		m.modelItems = msg.ModelList
		if msg.ProviderNames != nil {
			m.providerNames = msg.ProviderNames
		}
		m.refreshViewport()
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case AgentMsg:
		m.HandleAgentMsg(msg)
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

	case tea.MouseMsg:
		// Forward mouse events to viewport (wheel scroll).
		return m, m.viewportUpdate(msg)

	case tea.KeyPressMsg:
		return m.handleKeyPress(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
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
		if m.commandMode {
			m.input.SetValue("")
			m.clearCommandMode()
		}
		return nil

	case key.Matches(msg, keys.Help):
		if !m.commandMode && m.input.Value() == "" {
			m.showHelp = true
			m.adjustViewport()
		}
		return nil

	case key.Matches(msg, keys.Search):
		if !m.commandMode && m.input.Value() == "" {
			m.searchMode = true
			m.searchQuery = ""
			m.searchMatches = nil
			m.searchIdx = -1
		}
		return nil

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
		if m.thinking {
			return nil
		}
		m.thinkContent = ""
		m.reasoningExpanded = make(map[string]bool)
		value := m.input.Value()
		if strings.TrimSpace(value) == "" {
			return nil
		}
		m.AddMessage("user", value)
		m.SetThinking(true)
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
	m.input, cmd = m.input.Update(msg)
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
			m.input.ShowSuggestions = true
			m.updateCommandSuggestions()
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
	m.input.ShowSuggestions = false
	m.input.SetSuggestions(nil)
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
	available := m.height - m.headerHeight() - 4 // status, input, border/padding
	items := available - 4                       // border (2) + padding (2)
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
		available := m.height - m.headerHeight() - 5
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
		if rowCount > m.maxModelLines() {
			rowCount = m.maxModelLines()
		}
		return 4 + rowCount
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
// current terminal dimensions and command mode state.
func (m *Model) adjustViewport() {
	if m.height == 0 {
		return
	}
	chatHeight := m.height - m.headerHeight() - 3 // -3 for status line + footer
	if m.ephemMsg != "" {
		chatHeight-- // ephemeral message line
	}
	if m.commandMode || m.modelMode || m.questionMode || m.showHelp {
		chatHeight -= m.paletteLines()
	}
	if m.searchMode {
		chatHeight -= 1 // search indicator line
	}
	if chatHeight < 5 {
		chatHeight = 5
	}
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(chatHeight)
	m.refreshViewport()
}

// chatWrap wraps text to fit within the terminal width, accounting for a
// prefix label (e.g. "yaah: "). It returns the wrapped text with the prefix
// applied only to the first line.
func chatWrap(prefix, content string, width int) string {
	maxWidth := width - len(prefix)
	if maxWidth < 10 {
		maxWidth = 10
	}
	wrapped := wrapText(content, maxWidth)
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = prefix + line
		} else {
			lines[i] = strings.Repeat(" ", len(prefix)) + line
		}
	}
	return strings.Join(lines, "\n")
}

// wrapText performs simple word-wrapping at the given width, preserving
// explicit newlines in the source text.
func wrapText(text string, width int) string {
	var result strings.Builder
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			result.WriteString("\n")
			continue
		}
		wrapped := wrapParagraph(paragraph, width)
		result.WriteString(wrapped)
		result.WriteString("\n")
	}
	out := result.String()
	// Remove the trailing newline added by the last iteration
	return strings.TrimSuffix(out, "\n")
}

// wrapParagraph wraps a single line (no embedded newlines) to the given width.
func wrapParagraph(line string, width int) string {
	words := strings.Fields(line)
	if len(words) == 0 {
		return ""
	}
	var result strings.Builder
	lineLen := 0
	for i, word := range words {
		wLen := displayWidth(word)
		if i == 0 {
			result.WriteString(word)
			lineLen = wLen
		} else if lineLen+1+wLen > width {
			result.WriteString("\n")
			result.WriteString(word)
			lineLen = wLen
		} else {
			result.WriteString(" ")
			result.WriteString(word)
			lineLen += 1 + wLen
		}
	}
	return result.String()
}

// renderMessages produces the full chat content (messages, streaming,
// thinking indicator, tool call) as a single string suitable for
// handing to the viewport. Width is m.width; callers must ensure
// m.width is set.

// Reasoning content (from models like DeepSeek)

// Streaming content

// Thinking indicator (only when no reasoning text to show)

// renderModelPalette renders the model selection list above the input.

// Build display rows: provider heading + model items

// Find the display row index for the selected model

// Window calculation over display rows

// renderCommandPalette renders the command suggestion list above the input.

// renderQuestionModal renders the interactive question dialog.

// Window calculation: show options around the highlighted index

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
		return v
	}

	// Header: figlet banner + provider/model line (or compact if hidden)
	var header string
	if m.showBanner && m.banner != "" {
		header = m.banner + "\n\n" +
			titleStyle.Render(fmt.Sprintf("%s/%s", m.provider, m.modelName)) + "\n"
	} else {
		header = titleStyle.Render(fmt.Sprintf("yaah · %s/%s", m.provider, m.modelName)) + "\n\n"
	}

	// Status bar (1 line): message count + context bar only.
	// Provider/model is in the header; no need to duplicate.
	var statusText string
	ctxBar := ""
	if m.contextWindow > 0 {
		ctxBar = " " + contextBar(m.contextPct)
	}
	statusText = fmt.Sprintf(" %s │ messages: %d │%s",
		shortenCWD(m.cwd, m.width/3), len(m.messages), ctxBar)
	status := statusStyle.Width(m.width).Render(statusText)

	// Ephemeral message line (shown only when active, auto-clears)
	var ephemLine string
	if m.ephemMsg != "" {
		ephemLine = statusStyle.
			Foreground(lipgloss.Color("10")).
			Width(m.width).
			Render(m.ephemMsg)
	}

	// Viewport holds the scrollable chat history
	viewportView := m.viewport.View()

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
		palette = m.renderHelpOverlay()
	} else if m.questionMode {
		palette = m.renderQuestionModal()
	} else if m.modelMode {
		palette = m.renderModelPalette()
	} else if m.commandMode {
		palette = m.renderCommandPalette()
	}

	// Input (1 line)
	inputView := m.input.View()

	// Footer hint bar (1 line) — always visible with key shortcuts
	footer := m.help.View(footerKeyMap{})

	elements := []string{header, viewportView, status}
	if ephemLine != "" {
		elements = append(elements, ephemLine)
	}
	if palette != "" {
		elements = append(elements, palette)
	}
	if searchLine != "" {
		elements = append(elements, searchLine)
	}
	elements = append(elements, inputView, footer)
	body := lipgloss.JoinVertical(lipgloss.Left, elements...)

	v := tea.NewView(zone.Scan(body))
	v.AltScreen = true
	// Position the terminal cursor at the textinput's location.
	// The input line is above the footer (last line).
	if !m.input.VirtualCursor() {
		if c := m.input.Cursor(); c != nil {
			// input is the second-to-last element; footer is last.
			// If ephemeral message is shown, input is third-to-last.
			offset := 2
			if m.ephemMsg != "" {
				offset = 3
			}
			c.Y = m.height - offset
			v.Cursor = c
		}
	}
	return v
}

// renderHelpOverlay renders a full help screen with all keybindings.

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

// toolIndent prefixes each line of content with a dimmed border character
// for a clean left-margin treatment (no background needed).
func toolIndent(width int, content string) string {
	prefix := toolStyle.Render("│ ")
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
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
