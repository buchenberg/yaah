package tui

import (
	"strings"

	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

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
// Once the sub-agent's result is attached, the line becomes a zone-marked
// toggle that expands to show the result in a bordered box.
type SubAgentLine struct {
	zoneID    string
	role      string // role key used for color lookup (e.g. "checker")
	task      string
	running   bool
	duration  string // formatted duration (done/error only)
	errMsg    string // error text (error state only)
	result    string // final sub-agent result (enables the expand toggle)
	width     int
	maxHeight int // viewport height, used for the output box budget
	expanded  bool
}

// NewSubAgentLine creates a sub-agent line component.
func NewSubAgentLine(zoneID, role, task string, running bool, duration, errMsg, result string, width, maxHeight int, expanded bool) SubAgentLine {
	return SubAgentLine{
		zoneID:    zoneID,
		role:      role,
		task:      task,
		running:   running,
		duration:  duration,
		errMsg:    errMsg,
		result:    result,
		width:     width,
		maxHeight: maxHeight,
		expanded:  expanded,
	}
}

// Render returns the styled line with a trailing newline, plus the
// expanded output box beneath it when open.
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

	line := ""
	if c.result != "" {
		if c.expanded {
			line += "▼ "
		} else {
			line += "▶ "
		}
	}
	line += icon + " " + styled
	if c.task != "" {
		line += toggleStyle.Render(" · " + c.task)
	}
	if !c.running && c.duration != "" {
		line += toggleStyle.Render(" (" + c.duration + ")")
	}
	if c.errMsg != "" {
		line += errorStyle.Render(" — " + c.errMsg)
	}
	line = "  " + line

	var b strings.Builder
	if c.result != "" {
		b.WriteString(zone.Mark(c.zoneID, line))
	} else {
		b.WriteString(line)
	}
	b.WriteString("\n")
	if c.expanded && c.result != "" {
		b.WriteString(renderOutputBox(c.width, c.maxHeight, c.result))
		b.WriteString("\n")
	}
	return b.String()
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
