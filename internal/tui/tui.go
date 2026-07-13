// Package tui implements the terminal UI for yaah using bubbletea.
package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))
)

// Message represents a chat message in the TUI.
type Message struct {
	Role    string
	Content string
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
	provider      string
	modelName     string
	width         int
	height        int
	thinking      bool
	toolCall      string
	streaming     bool   // currently streaming a response
	streamContent string // accumulated streaming content
	onSubmit      func(string)
	onQuit        func()
}

// New creates a new TUI model.
func New(provider, model string, onSubmit func(string), onQuit func()) *Model {
	input := textinput.New()
	input.Placeholder = "Type a message..."
	input.Focus()
	input.CharLimit = 0
	input.SetWidth(80)

	vp := viewport.New()

	return &Model{
		input:     input,
		viewport:  vp,
		provider:  provider,
		modelName: model,
		onSubmit:  onSubmit,
		onQuit:    onQuit,
	}
}

// refreshViewport rebuilds the viewport content from the current message
// state and scrolls to the bottom so the user sees the latest message.
func (m *Model) refreshViewport() {
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
}

// AddMessage adds a message to the chat history.
func (m *Model) AddMessage(role, content string) {
	m.messages = append(m.messages, Message{Role: role, Content: content})
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
		m.AddMessage("assistant", msg.Flush)
		m.streaming = false
		m.streamContent = ""
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
			m.AddMessage("assistant", m.streamContent)
		} else if msg.Response != "" {
			m.AddMessage("assistant", msg.Response)
		}
		m.streaming = false
		m.streamContent = ""
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case AgentMsg:
		m.HandleAgentMsg(msg)
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(msg.Width - 4)
		// Layout: 2 (header) + chatArea + 1 (status) + 1 (input) = height
		chatHeight := msg.Height - 4
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

	// Render committed messages
	for _, msg := range m.messages {
		var rendered string
		switch msg.Role {
		case "user":
			rendered = userStyle.Render(chatWrap("you: ", msg.Content, m.width))
		case "assistant":
			rendered = assistantStyle.Render(chatWrap("yaah: ", msg.Content, m.width))
		case "tool":
			rendered = toolStyle.Render(chatWrap("  ", msg.Content, m.width))
		default:
			rendered = chatWrap("", msg.Content, m.width)
		}
		b.WriteString(rendered)
		b.WriteString("\n")
	}

	// Streaming content
	if m.streaming && m.streamContent != "" {
		rendered := assistantStyle.Render(chatWrap("yaah: ", m.streamContent, m.width))
		b.WriteString(rendered)
		b.WriteString("\n")
	}

	// Thinking indicator
	if m.thinking && !m.streaming {
		b.WriteString(spinnerStyle.Render("  ⠋ Thinking..."))
		b.WriteString("\n")
	}

	// Tool call display
	if m.toolCall != "" {
		b.WriteString(toolStyle.Render(fmt.Sprintf("  tool: %s", m.toolCall)))
		b.WriteString("\n")
	}

	return b.String()
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("Initializing...")
	}

	// Header (2 lines: content + blank)
	header := titleStyle.Render("yaah") + " " +
		fmt.Sprintf("%s/%s", m.provider, m.modelName) + "\n\n"

	// Status bar (1 line)
	status := statusStyle.Width(m.width).Render(
		fmt.Sprintf(" %s/%s │ messages: %d │ ctrl+c quit",
			m.provider, m.modelName, len(m.messages)),
	) + "\n"

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
	// Position the terminal cursor at the textinput's location
	// (only when not in virtual-cursor mode, which most terminals use).
	if !m.input.VirtualCursor() {
		if c := m.input.Cursor(); c != nil {
			v.Cursor = c
		}
	}
	return v
}
