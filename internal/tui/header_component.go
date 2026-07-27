package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// keyHintLines are the stacked right-justified keybinding hints in the header.
var keyHintLines = []string{
	": commands  / search  ? help",
	"ctrl+y copy  ctrl+c quit",
}

// Header renders the top-of-screen title block: the ASCII art banner
// plus provider/model line with keybinding hints stacked on the right side.
type Header struct {
	banner     string
	provider   string
	model      string
	showBanner bool
	width      int
}

// NewHeader creates a header component.
func NewHeader(banner, provider, model string, showBanner bool, width int) Header {
	return Header{
		banner:     banner,
		provider:   provider,
		model:      model,
		showBanner: showBanner,
		width:      width,
	}
}

// Render returns the header block with stacked keybinding hints right-justified.
func (h Header) Render() string {
	if h.showBanner && h.banner != "" {
		return h.banner + "\n\n" +
			h.renderStacked(titleStyle.Render(fmt.Sprintf("%s/%s", h.provider, h.model))) + "\n"
	}
	return h.renderStacked(
		titleStyle.Render(fmt.Sprintf("yaah · %s/%s", h.provider, h.model)),
	) + "\n\n"
}

// renderStacked produces a text block where the left content sits on the first
// line and key hints are right-aligned using plain spaces — no ANSI cursor
// positioning (which can corrupt terminal state in the viewport below).
func (h Header) renderStacked(left string) string {
	if h.width <= 0 {
		var lines []string
		lines = append(lines, left)
		for _, hint := range keyHintLines {
			lines = append(lines, commandDescStyle.Render(hint))
		}
		return strings.Join(lines, "\n")
	}

	leftWidth := lipgloss.Width(left)
	rightWidth := h.width - leftWidth
	if rightWidth < 0 {
		rightWidth = 0
	}

	var lines []string
	for i, hint := range keyHintLines {
		styled := commandDescStyle.Render(hint)
		hintWidth := lipgloss.Width(styled)
		pad := rightWidth - hintWidth
		if pad < 0 {
			pad = 0
		}
		if i == 0 {
			lines = append(lines, left+strings.Repeat(" ", pad)+styled)
		} else {
			lines = append(lines, strings.Repeat(" ", leftWidth+pad)+styled)
		}
	}
	return strings.Join(lines, "\n")
}
