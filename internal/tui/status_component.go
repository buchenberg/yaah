package tui

import "fmt"

// StatusBar renders the one-line status strip between the chat
// viewport and the input: shortened cwd, message count, and the
// context-window fill bar when a window is configured.
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

// Render returns the styled status line.
func (s StatusBar) Render() string {
	ctxBar := ""
	if s.hasContext {
		ctxBar = " " + contextBar(s.contextPct)
	}
	text := fmt.Sprintf(" %s │ messages: %d │%s",
		shortenCWD(s.cwd, s.width/3), s.messages, ctxBar)
	return statusStyle.Width(s.width).Render(text)
}
