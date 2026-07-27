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

// Header renders the top-of-screen title block as a two-column grid.
// Left: banner (when shown). Right: provider/model + keybinding hints.
//
// Columns are independent — adding content to one side does not affect the
// other. Vertical sizing is dynamic.
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
// Includes 2 lines for the rounded border (top + bottom).
func (h Header) Height() int {
	leftH := 0
	if h.showBanner && h.banner != "" {
		bannerLines := len(strings.Split(strings.TrimRight(h.banner, "\n"), "\n"))
		leftH = bannerLines + 1 // +1 for blank separator after banner
	}
	// Right column: provider line + key hints
	rightH := 1 + len(keyHintLines)
	contentH := leftH
	if rightH > contentH {
		contentH = rightH
	}
	return contentH + 2 // +2 for top/bottom border
}

// Render returns the header as a two-column grid wrapped in a rounded pink
// border. Left: banner. Right: provider/model + key hints.
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

	content := h.renderStacked(leftContent, leftWidth, rightContent)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(0, 1).
		Width(h.width).
		Render(content)
}

// renderLeft builds the left column: banner (if shown).
func (h Header) renderLeft() string {
	if h.showBanner && h.banner != "" {
		banner := strings.TrimRight(h.banner, "\n")
		return strings.Join(strings.Split(banner, "\n"), "\n")
	}
	return "yaah"
}

// renderRight builds the right column: provider/model at the top,
// followed by stacked keybinding hints, right-aligned.
func (h Header) renderRight(width int) string {
	aligner := lipgloss.NewStyle().Width(width).Align(lipgloss.Right)
	var lines []string

	// Provider/model line
	if h.showBanner && h.banner != "" {
		lines = append(lines, titleStyle.Render(h.provider+"/"+h.model))
	} else {
		lines = append(lines, titleStyle.Render("yaah · "+h.provider+"/"+h.model))
	}

	// Key hints
	for _, hint := range keyHintLines {
		styled := commandDescStyle.Render(hint)
		lines = append(lines, aligner.Render(styled))
	}
	return strings.Join(lines, "\n")
}

// renderStacked composes the two columns using plain-text concatenation
// with space padding. No ANSI cursor positioning — safe for viewport.
func (h Header) renderStacked(left string, leftWidth int, right string) string {
	leftLines := strings.Split(left, "\n")
	rightLines := strings.Split(right, "\n")

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
