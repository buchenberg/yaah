// Package tui implements the terminal UI for yaah using bubbletea.
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/buchenberg/yaah/internal/banner"
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39"))

	userStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15"))

	assistantStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	toolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39"))

	panelBorder = lipgloss.RoundedBorder()

	panelStyle = lipgloss.NewStyle().
			Border(panelBorder).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)

	copyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Italic(true)
)

// Message represents a chat message in the TUI.
type Message struct {
	Role    string
	Content string // glamour-rendered for assistant, raw for others
	Raw     string // original markdown (for copy), same as Content for user/tool
}

// AgentMsg is a message from the agent goroutine.
type AgentMsg struct {
	Token    string
	ToolName string
	Flush    string // streamed content to commit before a tool call
	Done     bool
	Response string
	Err      error
}

// Model is the bubbletea model for the yaah TUI.
type Model struct {
	messages      []Message
	viewport      viewport.Model
	input         textinput.Model
	spinner       spinner.Model
	mdRenderer    *glamour.TermRenderer
	banner        string // pre-rendered figlet + lolcat ASCII art
	provider      string
	modelName     string
	width         int
	height        int
	thinking      bool
	toolCall      string
	streaming     bool   // currently streaming a response
	streamContent string // accumulated streaming content
	copyFlash     string // brief "Copied!" indicator
	onSubmit      func(string)
	onQuit        func()
}

// clearCopyFlashMsg clears the copy flash indicator after a timeout.
type clearCopyFlashMsg struct{}

// New creates a new TUI model.
func New(provider, model string, onSubmit func(string), onQuit func()) *Model {
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
		input:     input,
		spinner:   sp,
		viewport:  vp,
		banner:    bn,
		provider:  provider,
		modelName: model,
		onSubmit:  onSubmit,
		onQuit:    onQuit,
	}
}

// createRenderer (re)creates the glamour markdown renderer for the current
// terminal width. Called on startup and on window resize.
func (m *Model) createRenderer() {
	width := m.width - 4
	if width < 20 {
		width = 20
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err == nil {
		m.mdRenderer = r
	}
}

// renderMarkdown renders raw markdown through glamour. Falls back to the
// raw string if the renderer is unavailable.
func (m *Model) renderMarkdown(content string) string {
	if m.mdRenderer == nil {
		return content
	}
	out, err := m.mdRenderer.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimSpace(out)
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
}

// reRenderMessages re-renders all assistant messages through the current
// glamour renderer (used on window resize when word-wrap width changes).
func (m *Model) reRenderMessages() {
	for i := range m.messages {
		if m.messages[i].Role == "assistant" && m.messages[i].Raw != "" {
			m.messages[i].Content = m.renderMarkdown(m.messages[i].Raw)
		}
	}
}

// headerHeight returns the number of lines the banner + provider header
// occupies. Used to size the viewport.
func (m *Model) headerHeight() int {
	header := m.banner + "\n\n" +
		titleStyle.Render(fmt.Sprintf("%s/%s", m.provider, m.modelName)) + "\n"
	return len(strings.Split(header, "\n"))
}

// refreshViewport rebuilds the viewport content from the current message
// state and scrolls to the bottom so the user sees the latest message.
func (m *Model) refreshViewport() {
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
}

// AddMessage adds a message to the chat history.
func (m *Model) AddMessage(role, content string) {
	m.messages = append(m.messages, Message{Role: role, Content: content, Raw: content})
	m.refreshViewport()
}

// SetThinking sets the thinking state.
func (m *Model) SetThinking(thinking bool) {
	m.thinking = thinking
	m.refreshViewport()
}

// SetToolCall sets the current tool call display.
func (m *Model) SetToolCall(name string) {
	m.toolCall = name
	m.refreshViewport()
}

// ClearToolCall clears the tool call display.
func (m *Model) ClearToolCall() {
	m.toolCall = ""
	m.refreshViewport()
}

// AppendToken appends a streaming token to the current response.
func (m *Model) AppendToken(token string) {
	if !m.streaming {
		m.streaming = true
		m.streamContent = ""
	}
	m.streamContent += token
	m.refreshViewport()
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
		// Commit the accumulated streaming content as a message so the
		// next segment (after a tool call) starts on a fresh line.
		m.streaming = false
		m.streamContent = ""
		m.AddAssistantMessage(msg.Flush)
		return
	}

	if msg.Token != "" {
		m.AppendToken(msg.Token)
		return
	}

	if msg.ToolName != "" {
		m.SetToolCall(msg.ToolName)
		return
	}

	if msg.Done {
		m.SetThinking(false)
		m.ClearToolCall()
		if m.streaming && m.streamContent != "" {
			content := m.streamContent
			m.streaming = false
			m.streamContent = ""
			m.AddAssistantMessage(content)
		} else if msg.Response != "" {
			m.AddAssistantMessage(msg.Response)
		}
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case AgentMsg:
		m.HandleAgentMsg(msg)
		return m, nil

	case spinner.TickMsg:
		var spinCmd tea.Cmd
		m.spinner, spinCmd = m.spinner.Update(msg)
		if m.thinking {
			m.refreshViewport()
		}
		return m, spinCmd

	case clearCopyFlashMsg:
		m.copyFlash = ""
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(msg.Width - 4)
		m.createRenderer()
		m.reRenderMessages()
		chatHeight := msg.Height - m.headerHeight() - 2
		if chatHeight < 5 {
			chatHeight = 5
		}
		m.viewport.SetWidth(msg.Width)
		m.viewport.SetHeight(chatHeight)
		m.refreshViewport()
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.onQuit != nil {
				m.onQuit()
			}
			return m, tea.Quit

		case "ctrl+y":
			for i := len(m.messages) - 1; i >= 0; i-- {
				if m.messages[i].Role == "assistant" && m.messages[i].Raw != "" {
					m.copyFlash = "Copied markdown to clipboard!"
					return m, tea.Batch(
						tea.SetClipboard(m.messages[i].Raw),
						tea.Tick(2*time.Second, func(time.Time) tea.Msg {
							return clearCopyFlashMsg{}
						}),
					)
				}
			}
			return m, nil

		case "enter":
			// Ignore input while thinking/streaming
			if m.thinking {
				return m, nil
			}
			value := m.input.Value()
			if strings.TrimSpace(value) == "" {
				return m, nil
			}
			m.AddMessage("user", value)
			m.SetThinking(true)
			m.input.SetValue("")
			if m.onSubmit != nil {
				m.onSubmit(value)
			}
			return m, nil

		}
	}

	m.input, cmd = m.input.Update(msg)
	return m, cmd
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
		wLen := len(word)
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
func (m *Model) renderMessages() string {
	var b strings.Builder

	panelW := m.width - 4

	for _, msg := range m.messages {
		switch msg.Role {
		case "user":
			rendered := userStyle.Render(chatWrap("you: ", msg.Content, m.width))
			b.WriteString(rendered)
			b.WriteString("\n\n")

		case "assistant":
			b.WriteString(assistantStyle.Render("yaah:"))
			b.WriteString("\n")
			copyText := copyStyle.Render("📋 ctrl+y to copy as md")
			inner := msg.Content + "\n\n" + copyText
			panel := panelStyle.Width(panelW).Render(inner)
			b.WriteString(panel)
			b.WriteString("\n\n")

		case "tool":
			rendered := toolStyle.Render(chatWrap("  ", msg.Content, m.width))
			b.WriteString(rendered)
			b.WriteString("\n")

		default:
			rendered := chatWrap("", msg.Content, m.width)
			b.WriteString(rendered)
			b.WriteString("\n")
		}
	}

	// Streaming content (not yet committed — no panel)
	if m.streaming && m.streamContent != "" {
		rendered := assistantStyle.Render(chatWrap("yaah: ", m.streamContent, m.width))
		b.WriteString(rendered)
		b.WriteString("\n")
	}

	// Thinking indicator
	if m.thinking && !m.streaming {
		rendered := spinnerStyle.Render(fmt.Sprintf("  %s Thinking...", m.spinner.View()))
		b.WriteString(rendered)
		b.WriteString("\n")
	}

	// Tool call display
	if m.toolCall != "" {
		rendered := toolStyle.Render(fmt.Sprintf("  tool: %s", m.toolCall))
		b.WriteString(rendered)
		b.WriteString("\n")
	}

	return b.String()
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("Initializing...")
	}

	// Header: figlet banner + blank line + provider/model line + trailing newline.
	header := m.banner + "\n\n" +
		titleStyle.Render(fmt.Sprintf("%s/%s", m.provider, m.modelName)) + "\n"

	// Status bar (1 line). No trailing \n — JoinVertical adds the separator.
	var statusText string
	if m.copyFlash != "" {
		statusText = " " + m.copyFlash
	} else {
		statusText = fmt.Sprintf(" %s/%s │ messages: %d │ ctrl+y copy as md │ ctrl+c quit",
			m.provider, m.modelName, len(m.messages))
	}
	status := statusStyle.Width(m.width).Render(statusText)

	// Viewport holds the scrollable chat history
	viewportView := m.viewport.View()

	// Input (1 line)
	inputView := m.input.View()

	body := lipgloss.JoinVertical(lipgloss.Left,
		header,
		viewportView,
		status,
		inputView,
	)

	v := tea.NewView(body)
	v.AltScreen = true
	// Position the terminal cursor at the textinput's location.
	// textinput.Cursor() returns Y=0 (relative to the widget), so we
	// offset it to the input line, which is the last line of the view.
	if !m.input.VirtualCursor() {
		if c := m.input.Cursor(); c != nil {
			c.Y = m.height - 1
			v.Cursor = c
		}
	}
	return v
}
