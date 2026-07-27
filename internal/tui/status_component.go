package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// StatusBar renders the one-line status strip between the chat
// viewport and the input: shortened cwd, message count, and
// context-window fill bar.
type StatusBar struct {
	cwd        string
	messages   int
	contextPct int
	hasContext bool
	width      int
}

// NewStatusBar creates a status bar component.
func NewStatusBar(cwd string, messages, contextPct int, hasContext bool, width int) StatusBar {
	return StatusBar{
		cwd:        cwd,
		messages:   messages,
		contextPct: contextPct,
		hasContext: hasContext,
		width:      width,
	}
}

// Height returns the number of visual lines the rendered bar occupies.
func (s StatusBar) Height() int {
	return 3 // 1 content + 2 border
}

// Render returns the bordered status line.
func (s StatusBar) Render() string {
	ctxBar := ""
	if s.hasContext {
		ctxBar = " " + contextBar(s.contextPct)
	}

	text := fmt.Sprintf(" %s │ messages: %d │%s",
		shortenCWD(s.cwd, s.width/3), s.messages, ctxBar)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Width(s.width).
		Render(text)
}
