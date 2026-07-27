package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// InfoBar renders a bar between the header and viewport showing the
// current active prompt with a rainbow spinner when the agent is busy.
// When idle, the prompt text is shown in blue.
type InfoBar struct {
	prompt     string
	activeView string
	width      int
}

var infoBarPromptStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("39"))

// NewInfoBar creates an info bar component.
func NewInfoBar(prompt, activeView string, width int) InfoBar {
	return InfoBar{
		prompt:     prompt,
		activeView: activeView,
		width:      width,
	}
}

// Height returns the number of visual lines the rendered bar occupies.
// 3 lines: blank above, prompt, blank below — same spacing as the
// bordered version but without visible borders.
func (b InfoBar) Height() int {
	return 3
}

// Render returns the info bar content with vertical padding.
func (b InfoBar) Render() string {
	if b.prompt == "" {
		return "\n\n" // 3 lines of empty space to match Height()
	}
	var label string
	if b.activeView != "" {
		spinner := lolcatRender(b.activeView)
		label = spinner + " " + infoBarPromptStyle.Render(truncatePrompt(b.prompt, b.width-6))
	} else {
		label = infoBarPromptStyle.Render(truncatePrompt(b.prompt, b.width-4))
	}
	return "\n" + label + "\n"
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
