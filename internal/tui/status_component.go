package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/buchenberg/yaah/internal/banner"
)

// StatusBar renders the one-line status strip between the chat
// viewport and the input: shortened cwd, message count, context-window
// fill bar, and a spinning rainbow indicator when the agent is active.
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

// Render returns the styled status line.
func (s StatusBar) Render() string {
	ctxBar := ""
	if s.hasContext {
		ctxBar = " " + contextBar(s.contextPct)
	}

	left := fmt.Sprintf(" %s │ messages: %d │%s",
		shortenCWD(s.cwd, s.width/3), s.messages, ctxBar)

	indicator := ""
	if s.activeView != "" {
		indicator = " " + banner.Lolcat(s.activeView)
	}

	leftW := lipgloss.Width(left)
	suffixW := lipgloss.Width(indicator)
	pad := s.width - leftW - suffixW
	if pad < 0 {
		pad = 0
	}
	return statusStyle.Width(s.width).Render(left + strings.Repeat(" ", pad) + indicator)
}
