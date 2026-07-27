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
// Left: banner (when shown). Right: provider/model, MCP status, key hints.
// The entire block is wrapped in a rounded pink border.
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

// outerBorder returns the lipgloss style for the outer header border.
func outerBorder(w int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Width(w)
}

// ViewportBorder returns the lipgloss style for the viewport border.
func ViewportBorder(w int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("99")).
		Width(w)
}

// Height returns the number of visual lines the rendered header occupies.
func (h Header) Height() int {
	outerOverhead := 4 // 2 border + 2 padding

	leftLines := h.leftContentLines()
	rightLines := h.rightContentLines()
	cellH := max(leftLines, rightLines)
	return cellH + outerOverhead
}

func (h Header) leftContentLines() int {
	if h.showBanner && h.banner != "" {
		return len(strings.Split(strings.TrimRight(h.banner, "\n"), "\n"))
	}
	return 0
}

func (h Header) rightContentLines() int {
	lines := 1 // provider/model
	if len(h.mcpInfos) > 0 {
		lines += 1 + len(h.mcpInfos) // blank line + mcp entries
	}
	lines += len(keyHintLines)
	return lines
}

// Render returns the header as two columns inside a rounded pink border.
func (h Header) Render() string {
	if h.width < 10 {
		h.width = 10
	}

	innerW := h.width - 4 // outer border + padding
	gap := 2

	cellTotal := innerW - gap
	leftW := cellTotal * 2 / 3
	rightW := cellTotal - leftW
	if rightW < 1 {
		rightW = 1
	}

	leftContent := h.renderLeft()
	rightContent := h.renderRight(rightW)

	// Place columns side by side with plain-text concatenation.
	// No ANSI cursor positioning — safe for viewport rendering.
	leftLines := strings.Split(leftContent, "\n")
	rightLines := strings.Split(rightContent, "\n")
	maxH := max(len(leftLines), len(rightLines))

	var combined []string
	for i := 0; i < maxH; i++ {
		left := ""
		if i < len(leftLines) {
			left = leftLines[i]
		}
		right := ""
		if i < len(rightLines) {
			right = rightLines[i]
		}
		pad := leftW + gap - lipgloss.Width(left)
		if pad < 1 {
			pad = 1
		}
		combined = append(combined, left+strings.Repeat(" ", pad)+right)
	}

	return outerBorder(h.width).Render(strings.Join(combined, "\n"))
}

// renderLeft builds the left column content.
func (h Header) renderLeft() string {
	if !h.showBanner || h.banner == "" {
		return ""
	}
	banner := strings.TrimRight(h.banner, "\n")
	return strings.Join(strings.Split(banner, "\n"), "\n")
}

// renderRight builds the right column content, right-aligned.
// Order: provider/model, MCP server status, keybinding hints.
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
