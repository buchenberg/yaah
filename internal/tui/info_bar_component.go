package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// InfoBar renders a bar between the header and viewport showing the
// current active prompt with a rainbow spinner when the agent is busy.
// When idle, the bar renders as an empty bordered box.
type InfoBar struct {
	prompt     string
	activeView string
	width      int
}

// NewInfoBar creates an info bar component.
func NewInfoBar(prompt, activeView string, width int) InfoBar {
	return InfoBar{
		prompt:     prompt,
		activeView: activeView,
		width:      width,
	}
}

// Height returns the number of visual lines the rendered bar occupies.
func (b InfoBar) Height() int {
	return 3 // 1 content + 2 border
}

// Render returns the bordered info bar.
func (b InfoBar) Render() string {
	label := ""
	if b.prompt != "" {
		if b.activeView != "" {
			spinner := lolcatRender(b.activeView)
			label = spinner + " " + truncatePrompt(b.prompt, b.width-6)
		} else {
			label = truncatePrompt(b.prompt, b.width-4)
		}
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Width(b.width).
		Render(label)
}

// truncatePrompt shortens a prompt to fit within maxW, adding ellipsis.
func truncatePrompt(s string, maxW int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	runes := []rune(s)
	w := 0
	for i, r := range runes {
		width := 1
		if lipgloss.Width(string(r)) > 1 {
			width = 2
		}
		if w+width+3 > maxW {
			return string(runes[:i]) + "..."
		}
		w += width
	}
	return s
}
