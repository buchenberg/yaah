package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// StatusBar renders the one-line status strip between the chat
// viewport and the input: a spinner indicator when active, shortened
// cwd, message count, and context-window fill bar.
type StatusBar struct {
	cwd        string
	messages   int
	contextPct int
	hasContext bool
	width      int
	activeView string // spinner view when active, empty when idle
}

// NewStatusBar creates a status bar component.
func NewStatusBar(cwd string, messages, contextPct int, hasContext bool, width int, activeView string) StatusBar {
	return StatusBar{
		cwd:        cwd,
		messages:   messages,
		contextPct: contextPct,
		hasContext: hasContext,
		width:      width,
		activeView: activeView,
	}
}

// Height returns the number of visual lines the rendered status bar occupies.
// With a border: 1 content line + 2 border lines = 3.
// Without a border (legacy): 1 content line.
func (s StatusBar) Height() int {
	return 3 // content (1) + border top (1) + border bottom (1)
}
func (s StatusBar) Render() string {
	indicator := ""
	if s.activeView != "" {
		indicator = lolcatRender(s.activeView) + " "
	}

	ctxBar := ""
	if s.hasContext {
		ctxBar = " " + contextBar(s.contextPct)
	}

	text := fmt.Sprintf("%s%s │ messages: %d │%s",
		indicator, shortenCWD(s.cwd, s.width/3), s.messages, ctxBar)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Width(s.width).
		Render(statusStyle.Render(text))
}
