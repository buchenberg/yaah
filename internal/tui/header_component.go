package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/buchenberg/yaah/internal/mcp"
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
	mcpInfos   []mcp.ServerInfo
}

// NewHeader creates a header component.
func NewHeader(banner, provider, model string, showBanner bool, width int, mcpInfos []mcp.ServerInfo) Header {
	return Header{
		banner:     banner,
		provider:   provider,
		model:      model,
		showBanner: showBanner,
		width:      width,
		mcpInfos:   mcpInfos,
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
		BorderForeground(lipgloss.Color("99")).
		Width(w)
}

// Height returns the number of visual lines the rendered header occupies.
func (h Header) Height() int {
	cellOverhead := 4
	outerOverhead := 4

	leftLines := h.leftContentLines()
	rightLines := len(keyHintLines) + len(h.mcpInfos)
	if len(h.mcpInfos) > 0 {
		rightLines++ // blank separator above key hints
	}
	if !h.showBanner || h.banner == "" {
		rightLines++ // provider line in right column
	}
	cellH := max(leftLines, rightLines) + cellOverhead
	return cellH + outerOverhead
}

func (h Header) leftContentLines() int {
	if h.showBanner && h.banner != "" {
		bannerLines := len(strings.Split(strings.TrimRight(h.banner, "\n"), "\n"))
		return bannerLines + 1 // just banner + blank separator
	}
	return 0
}

// Render returns the header as two bordered cells inside an outer border.
func (h Header) Render() string {
	if h.width < 10 {
		h.width = 10
	}

	outerWidth := h.width - 4
	gap := 2
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
		rightLines = append(rightLines, strings.Repeat(" ", rightCellW))
	}
	for len(leftLines) < len(rightLines) {
		leftLines = append(leftLines, strings.Repeat(" ", leftCellW))
	}

	var combined []string
	for i := 0; i < len(leftLines); i++ {
		combined = append(combined, leftLines[i]+strings.Repeat(" ", gap)+rightLines[i])
	}

	return outerBorderStyle(h.width).Render(strings.Join(combined, "\n"))
}

// renderLeft builds the left column content (no borders).
func (h Header) renderLeft() string {
	if !h.showBanner || h.banner == "" {
		return ""
	}
	banner := strings.TrimRight(h.banner, "\n")
	return strings.Join(strings.Split(banner, "\n"), "\n")
}

// renderRight builds the right column content (no borders).
// Shows MCP server status above keybinding hints, right-aligned.
func (h Header) renderRight(width int) string {
	aligner := lipgloss.NewStyle().Width(width).Align(lipgloss.Right)
	var lines []string

	// Provider/model line
	if h.showBanner && h.banner != "" {
		lines = append(lines, aligner.Render(titleStyle.Render(h.provider+"/"+h.model)))
	} else {
		lines = append(lines, aligner.Render(titleStyle.Render("yaah · "+h.provider+"/"+h.model)))
	}

	// MCP server status
	if len(h.mcpInfos) > 0 {
		lines = append(lines, "")
		for _, info := range h.mcpInfos {
			status := "✓"
			s := mcpStatusConnected
			if !info.Connected {
				status = "✗"
				s = mcpStatusDisconnect
			}
			line := fmt.Sprintf("%s %s (%s)", status, info.Name, info.Transport)
			lines = append(lines, aligner.Render(s.Render(line)))
		}
	}

	// Keybinding hints
	for _, hint := range keyHintLines {
		styled := commandDescStyle.Render(hint)
		lines = append(lines, aligner.Render(styled))
	}
	return strings.Join(lines, "\n")
}
