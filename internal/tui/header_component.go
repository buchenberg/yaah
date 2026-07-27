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

// cellBorderStyle returns a lipgloss style for inner cell borders.
func cellBorderStyle(w int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(w)
}

// outerBorderStyle returns the lipgloss style for the outer header border.
func outerBorderStyle(w int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Width(w)
}

// viewportBorderStyle returns the lipgloss style for the viewport border.
func viewportBorderStyle(w int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Width(w)
}

// Height returns the number of visual lines the rendered header occupies.
func (h Header) Height() int {
	// Inner cells each add 2 border + 2 padding = 4 lines overhead.
	// Outer header adds another 2 border + 2 padding = 4 lines.
	cellOverhead := 4
	outerOverhead := 4

	leftLines := h.leftContentLines()
	rightLines := len(keyHintLines) + 1 // key hints + provider line
	cellH := max(leftLines, rightLines) + cellOverhead
	return cellH + outerOverhead
}

func (h Header) leftContentLines() int {
	if h.showBanner && h.banner != "" {
		bannerLines := len(strings.Split(strings.TrimRight(h.banner, "\n"), "\n"))
		return bannerLines + 1 + 1 // banner + blank line + provider line
	}
	return 1 // provider line
}

// Render returns the header as two bordered cells inside an outer border.
func (h Header) Render() string {
	if h.width < 10 {
		h.width = 10
	}

	// Outer border consumes 4 chars width (2 border + 2 padding).
	outerWidth := h.width - 4
	// Gap between the two inner cells.
	gap := 2
	// Each inner cell border consumes 4 chars width (2 border + 2 padding).
	cellBorderW := 4

	cellTotal := outerWidth - gap
	leftCellW := cellTotal * 2 / 3
	rightCellW := cellTotal - leftCellW

	rightInnerW := max(1, rightCellW-cellBorderW)

	leftContent := h.renderLeft()
	rightContent := h.renderRight(rightInnerW)

	leftCell := cellBorderStyle(leftCellW).Render(leftContent)
	rightCell := cellBorderStyle(rightCellW).Render(rightContent)

	// Balance heights so cells stack evenly.
	leftLines := strings.Split(leftCell, "\n")
	rightLines := strings.Split(rightCell, "\n")
	for len(rightLines) < len(leftLines) {
		rightCell += "\n" + strings.Repeat(" ", rightCellW)
		rightLines = append(rightLines, "")
	}
	for len(leftLines) < len(rightLines) {
		leftCell += "\n" + strings.Repeat(" ", leftCellW)
		leftLines = append(leftLines, "")
	}

	// Place cells side by side with plain-text concatenation.
	var combined []string
	for i := 0; i < len(leftLines); i++ {
		combined = append(combined, leftLines[i]+strings.Repeat(" ", gap)+rightLines[i])
	}

	return outerBorderStyle(h.width).Render(strings.Join(combined, "\n"))
}

// renderLeft builds the left column content (no borders).
func (h Header) renderLeft() string {
	var lines []string
	if h.showBanner && h.banner != "" {
		banner := strings.TrimRight(h.banner, "\n")
		lines = append(lines, strings.Split(banner, "\n")...)
		lines = append(lines, "") // separator
		lines = append(lines, titleStyle.Render(h.provider+"/"+h.model))
	} else {
		lines = append(lines, titleStyle.Render("yaah · "+h.provider+"/"+h.model))
	}
	return strings.Join(lines, "\n")
}

// renderRight builds the right column content (no borders).
func (h Header) renderRight(width int) string {
	aligner := lipgloss.NewStyle().Width(width).Align(lipgloss.Right)
	var lines []string
	for _, hint := range keyHintLines {
		styled := commandDescStyle.Render(hint)
		lines = append(lines, aligner.Render(styled))
	}
	return strings.Join(lines, "\n")
}
