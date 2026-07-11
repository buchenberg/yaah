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
	Done     bool
	Response string
	Err      error
}

// Model is the bubbletea model for the yaah TUI.
type Model struct {
	messages      []Message
	scroll        int
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
	m.scrollToBottom()
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

func (m *Model) scrollToBottom() {
	m.scroll = len(m.messages)
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
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

		case tea.KeyUp:
			if m.scroll > 0 {
				m.scroll--
			}
			return m, nil

		case tea.KeyDown:
			if m.scroll < len(m.messages) {
				m.scroll++
			}
			return m, nil
		}
	}

	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View implements tea.Model.
func (m *Model) View() string {
	if m.width == 0 {
		return "Initializing..."
	}

	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render("yaah"))
	b.WriteString(" ")
	b.WriteString(fmt.Sprintf("%s/%s", m.provider, m.modelName))
	b.WriteString("\n\n")

	// Messages area
	chatHeight := m.height - 6
	if chatHeight < 5 {
		chatHeight = 5
	}

	// Render visible messages
	start := m.scroll - chatHeight
	if start < 0 {
		start = 0
	}
	end := m.scroll
	if end > len(m.messages) {
		end = len(m.messages)
	}

	for i := start; i < end; i++ {
		msg := m.messages[i]
		switch msg.Role {
		case "user":
			b.WriteString(userStyle.Render("you: " + msg.Content))
		case "assistant":
			b.WriteString(assistantStyle.Render("yaah: " + msg.Content))
		case "tool":
			b.WriteString(toolStyle.Render("  " + msg.Content))
		default:
			b.WriteString(msg.Content)
		}
		b.WriteString("\n")
	}

	// Streaming content (current response being streamed)
	if m.streaming && m.streamContent != "" {
		b.WriteString(assistantStyle.Render("yaah: " + m.streamContent))
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

	// Pad to fill screen
	linesRendered := end - start + 2
	if m.thinking && !m.streaming {
		linesRendered++
	}
	if m.streaming {
		linesRendered++
	}
	for i := linesRendered; i < chatHeight; i++ {
		b.WriteString("\n")
	}

	// Status bar
	b.WriteString(statusStyle.Width(m.width).Render(
		fmt.Sprintf(" %s/%s │ messages: %d │ ↑↓ scroll │ ctrl+c quit",
			m.provider, m.modelName, len(m.messages)),
	))
	b.WriteString("\n")

	// Input
	b.WriteString(m.input.View())
	b.WriteString("\n")

	return b.String()
}
