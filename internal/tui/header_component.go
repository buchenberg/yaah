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
// Used by the model to size the viewport. Includes 2 lines for the rounded
// border (top + bottom).
func (h Header) Height() int {
	leftH := 0
	if h.showBanner && h.banner != "" {
		bannerLines := len(strings.Split(strings.TrimRight(h.banner, "\n"), "\n"))
		leftH += bannerLines + 1 // +1 for blank separator after banner
	}
	leftH++ // provider/model line
	rightH := len(keyHintLines)
	contentH := leftH
	if rightH > leftH {
		contentH = rightH
	}
	return contentH + 2 // +2 for top/bottom border
}

// Render returns the header as a two-column grid wrapped in a rounded pink
// border (matching the input area). No trailing newline is added.
func (h Header) Render() string {
	innerWidth := h.width - 4
	if innerWidth < 1 {
		innerWidth = 1
	}

	leftContent := h.renderLeft()
	leftWidth := lipgloss.Width(leftContent)

	rightWidth := innerWidth - leftWidth
	if rightWidth < 1 {
		rightWidth = 1
	}
	rightContent := h.renderRight(rightWidth)

	// Use plain-text concatenation with space padding to combine the two
	// columns — no ANSI cursor positioning (which lipgloss.JoinHorizontal
	// uses internally and corrupts terminal state).
	content := h.renderStacked(leftContent, leftWidth, rightContent)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(0, 1).
		Width(h.width).
		Render(content)
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

// renderStacked composes the two columns using plain-text concatenation
// with space padding. First line places left content and right-hint side
// by side; subsequent lines pad the left area with spaces so the right
// column aligns vertically. No ANSI cursor positioning — safe for viewport.
func (h Header) renderStacked(left string, leftWidth int, right string) string {
	leftLines := strings.Split(left, "\n")
	rightLines := strings.Split(right, "\n")

	// If left is taller than right, pad right with empty lines.
	for len(rightLines) < len(leftLines) {
		rightLines = append(rightLines, "")
	}

	var lines []string
	for i, rl := range rightLines {
		rw := lipgloss.Width(rl)
		pad := h.width - 4 - leftWidth - rw
		if pad < 1 {
			pad = 1
		}
		if i < len(leftLines) {
			lines = append(lines, leftLines[i]+strings.Repeat(" ", pad)+rl)
		} else {
			lines = append(lines, strings.Repeat(" ", leftWidth+pad)+rl)
		}
	}
	return strings.Join(lines, "\n")
}
