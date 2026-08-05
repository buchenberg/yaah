package tui

import "github.com/buchenberg/yaah/internal/agent/subagent"

// UserMessage renders a user chat message: bold user-colored text
// wrapped to the terminal width on the user background.
type UserMessage struct {
	content string
	width   int
}

// NewUserMessage creates a user message component.
func NewUserMessage(content string, width int) UserMessage {
	return UserMessage{content: content, width: width}
}

// Render returns the wrapped, styled user message with a trailing newline.
func (c UserMessage) Render() string {
	return chatBubble(c.content, c.width, userStyle, userBgStyle) + "\n"
}

// AssistantMessage renders assistant chat content in the assistant
// foreground color. Trailing newlines are handled by the caller so the
// component composes with reasoning sections and streaming output.
type AssistantMessage struct {
	content string
}

// NewAssistantMessage creates an assistant message component.
func NewAssistantMessage(content string) AssistantMessage {
	return AssistantMessage{content: content}
}

// Render returns the styled assistant content.
func (c AssistantMessage) Render() string {
	return assistantStyle.Render(c.content)
}

// SubAgentLine renders a sub-agent's whole lifetime on a single line,
// tool-style: a status icon (⏳ running, ✓ done, ✗ error), a robot icon,
// the role-colored display name, and the task. The start event renders
// the running form; the end event updates the same message in place.
type SubAgentLine struct {
	role     string // role key used for color lookup (e.g. "checker")
	task     string
	running  bool
	duration string // formatted duration (done/error only)
	errMsg   string // error text (error state only)
}

// NewSubAgentLine creates a sub-agent line component.
func NewSubAgentLine(role, task string, running bool, duration, errMsg string) SubAgentLine {
	return SubAgentLine{role: role, task: task, running: running, duration: duration, errMsg: errMsg}
}

// Render returns the styled line with a trailing newline.
func (c SubAgentLine) Render() string {
	name := subagent.RoleDisplayName(subagent.SubAgentRole(c.role))
	label := name
	if name != c.role {
		label = name + " (" + c.role + ")"
	}
	styled := roleStyle(c.role).Render("🤖 " + label)

	icon := "✓"
	if c.running {
		icon = "⏳"
	} else if c.errMsg != "" {
		icon = "✗"
	}

	line := "  " + icon + " " + styled
	if c.task != "" {
		line += toggleStyle.Render(" · " + c.task)
	}
	if !c.running && c.duration != "" {
		line += toggleStyle.Render(" (" + c.duration + ")")
	}
	if c.errMsg != "" {
		line += errorStyle.Render(" — " + c.errMsg)
	}
	return line + "\n"
}

// SystemMessage renders a system or other-role message: wrapped text
// in the system foreground on the system background.
type SystemMessage struct {
	content string
	width   int
}

// NewSystemMessage creates a system message component.
func NewSystemMessage(content string, width int) SystemMessage {
	return SystemMessage{content: content, width: width}
}

// Render returns the wrapped, styled system message with a trailing newline.
func (c SystemMessage) Render() string {
	return chatBubble(c.content, c.width, systemStyle, systemBgStyle) + "\n"
}
