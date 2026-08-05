package tui

import (
	"fmt"
	"os"
	"strings"
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
	return 1
}

// Render returns the styled status line.
func (s StatusBar) Render() string {
	ctxBar := ""
	if s.hasContext {
		ctxBar = " " + contextBar(s.contextPct)
	}

	leftText := fmt.Sprintf(" %s │ messages: %d │%s",
		shortenCWD(s.cwd, s.width/3), s.messages, ctxBar)

	return statusStyle.Width(s.width).Render(leftText)
}

// shortenCWD returns the current working directory with $HOME replaced
// by ~, truncated to maxLen if longer.
func shortenCWD(cwd string, maxLen int) string {
	home, _ := os.UserHomeDir()
	s := cwd
	if home != "" && strings.HasPrefix(s, home) {
		s = "~" + s[len(home):]
	}
	if len(s) > maxLen && maxLen > 3 {
		s = "..." + s[len(s)-(maxLen-3):]
	}
	return s
}

// contextBar returns a 10-segment bar showing fill percentage.
func contextBar(pct int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	segments := 10
	filled := (pct*segments + 50) / 100 // round to nearest
	if filled == 0 && pct > 0 {
		filled = 1 // show at least one segment for non-zero
	}
	if filled > segments {
		filled = segments
	}
	empty := segments - filled
	if filled >= 8 {
		return fmt.Sprintf("[%s%s %d%%]", strings.Repeat("█", filled), strings.Repeat("░", empty), pct)
	}
	if filled >= 5 {
		return fmt.Sprintf("[%s%s %d%%]", strings.Repeat("▓", filled), strings.Repeat("░", empty), pct)
	}
	return fmt.Sprintf("[%s%s %d%%]", strings.Repeat("█", filled), strings.Repeat("░", empty), pct)
}
