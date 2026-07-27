package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// keyHintLines are the stacked right-justified keybinding hints in the header.
var keyHintLines = []string{
	": commands",
	"/ search",
	"? help",
	"ctrl+y copy",
	"ctrl+c quit",
}

// Header renders the top-of-screen title block as a two-column grid:
//
//	┌──────────────────────────┬──────────────┐
//	│  Banner (optional)       │  : commands  │
//	│                          │  / search    │
//	│  provider/model          │  ? help      │
//	│                          │  ctrl+y copy │
//	│                          │  ctrl+c quit │
//	└──────────────────────────┴──────────────┘
//
// Left column: banner (optional) + provider/model.
// Right column: stacked keybinding hints, right-aligned within the column.
//
// Columns are independent — adding content to one side does not affect the
// other. Vertical sizing is dynamic: the header height is
// max(left_height, right_height).
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

// Height returns the number of visual lines the rendered header occupies.
// Used by the model to size the viewport.
func (h Header) Height() int {
	leftH := 0
	if h.showBanner && h.banner != "" {
		bannerLines := len(strings.Split(strings.TrimRight(h.banner, "\n"), "\n"))
		leftH += bannerLines + 1 // +1 for blank separator after banner
	}
	leftH++ // provider/model line
	rightH := len(keyHintLines)
	if leftH > rightH {
		return leftH
	}
	return rightH
}

// Render returns the header as a two-column grid. The left column holds the
// banner and provider/model; the right column holds the stacked key hints.
// No trailing whitespace or padding newlines are added.
func (h Header) Render() string {
	leftContent := h.renderLeft()
	leftWidth := lipgloss.Width(leftContent)

	rightWidth := h.width - leftWidth
	if rightWidth < 1 {
		rightWidth = 1
	}
	rightContent := h.renderRight(rightWidth)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftContent, rightContent)
}

// renderLeft builds the left column: banner (if shown), blank separator,
// and the provider/model line.
func (h Header) renderLeft() string {
	var lines []string
	if h.showBanner && h.banner != "" {
		banner := strings.TrimRight(h.banner, "\n")
		lines = append(lines, strings.Split(banner, "\n")...)
		lines = append(lines, "") // visual separator
		lines = append(lines, titleStyle.Render(h.provider+"/"+h.model))
	} else {
		lines = append(lines, titleStyle.Render("yaah · "+h.provider+"/"+h.model))
	}
	return strings.Join(lines, "\n")
}

// renderRight builds the right column: each key hint right-aligned within
// the given column width, stacked vertically.
func (h Header) renderRight(width int) string {
	aligner := lipgloss.NewStyle().Width(width).Align(lipgloss.Right)
	var lines []string
	for _, hint := range keyHintLines {
		styled := commandDescStyle.Render(hint)
		lines = append(lines, aligner.Render(styled))
	}
	return strings.Join(lines, "\n")
}
