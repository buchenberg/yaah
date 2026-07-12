// Package tui implements the terminal UI for yaah using bubbletea.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	input.Width = 80

	return &Model{
		input:     input,
		provider:  provider,
		modelName: model,
		onSubmit:  onSubmit,
		onQuit:    onQuit,
	}
}

// AddMessage adds a message to the chat history.
func (m *Model) AddMessage(role, content string) {
	m.messages = append(m.messages, Message{Role: role, Content: content})
}

// SetThinking sets the thinking state.
func (m *Model) SetThinking(thinking bool) {
	m.thinking = thinking
}

// SetToolCall sets the current tool call display.
func (m *Model) SetToolCall(name string) {
	m.toolCall = name
}

// ClearToolCall clears the tool call display.
func (m *Model) ClearToolCall() {
	m.toolCall = ""
}

// AppendToken appends a streaming token to the current response.
func (m *Model) AppendToken(token string) {
	if !m.streaming {
		m.streaming = true
		m.streamContent = ""
	}
	m.streamContent += token
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
		m.input.Width = msg.Width - 4
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			if m.onQuit != nil {
				m.onQuit()
			}
			return m, tea.Quit

		case tea.KeyEnter:
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

// countLines counts the number of visual lines a string will occupy.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// View implements tea.Model.
func (m *Model) View() string {
	if m.width == 0 {
		return "Initializing..."
	}

	var b strings.Builder

	// Header (2 lines: content + blank)
	b.WriteString(titleStyle.Render("yaah"))
	b.WriteString(" ")
	b.WriteString(fmt.Sprintf("%s/%s", m.provider, m.modelName))
	b.WriteString("\n\n")

	// Available height for chat + status + input
	// Layout: 2 (header) + chatArea + 1 (status) + 1 (input) = height
	chatHeight := m.height - 4
	if chatHeight < 5 {
		chatHeight = 5
	}

	// Build all rendered blocks (messages + streaming + status indicators)
	// so we can count actual line usage and scroll correctly.
	type block struct {
		rendered string
		lines    int
	}
	var blocks []block

	// Render messages
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
		blocks = append(blocks, block{rendered, countLines(rendered)})
	}

	// Streaming content
	if m.streaming && m.streamContent != "" {
		rendered := assistantStyle.Render(chatWrap("yaah: ", m.streamContent, m.width))
		blocks = append(blocks, block{rendered, countLines(rendered)})
	}

	// Thinking indicator
	if m.thinking && !m.streaming {
		rendered := spinnerStyle.Render("  ⠋ Thinking...")
		blocks = append(blocks, block{rendered, 1})
	}

	// Tool call display
	if m.toolCall != "" {
		rendered := toolStyle.Render(fmt.Sprintf("  tool: %s", m.toolCall))
		blocks = append(blocks, block{rendered, 1})
	}

	// Determine which blocks to show to fit within chatHeight (show most recent)
	linesUsed := 0
	startBlock := len(blocks)
	for i := len(blocks) - 1; i >= 0; i-- {
		blockLines := blocks[i].lines + 1
		if linesUsed+blockLines > chatHeight {
			break
		}
		linesUsed += blockLines
		startBlock = i
	}

	// Render visible blocks
	for i := startBlock; i < len(blocks); i++ {
		b.WriteString(blocks[i].rendered)
		b.WriteString("\n")
	}

	// Pad remaining space to push status bar + input to the bottom
	for i := linesUsed; i < chatHeight; i++ {
		b.WriteString("\n")
	}

	// Status bar (1 line)
	b.WriteString(statusStyle.Width(m.width).Render(
		fmt.Sprintf(" %s/%s │ messages: %d │ ctrl+c quit",
			m.provider, m.modelName, len(m.messages)),
	))
	b.WriteString("\n")

	// Input (1 line)
	b.WriteString(m.input.View())

	return b.String()
}
