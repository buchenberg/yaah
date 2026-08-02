package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/buchenberg/yaah/internal/mcp"
)

// Header renders the top-of-screen title block as a two-column grid.
// Left: banner (when shown). Right: provider/model and MCP status.
// The entire block is wrapped in a rounded pink border.
type Header struct {
	banner     string
	provider   string
	model      string
	showBanner bool
	width      int
	mcpInfos   []mcp.ServerInfo
	version    string
}

// NewHeader creates a header component.
func NewHeader(banner, provider, model string, showBanner bool, width int, mcpInfos []mcp.ServerInfo, version string) Header {
	return Header{
		banner:     banner,
		provider:   provider,
		model:      model,
		showBanner: showBanner,
		width:      width,
		mcpInfos:   mcpInfos,
		version:    version,
	}
}

// Height returns the number of visual lines the rendered header occupies.
func (h Header) Height() int {
	outerOverhead := 0

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
	if h.version != "" {
		lines += 1
	}
	if len(h.mcpInfos) > 0 {
		lines += 1 + len(h.mcpInfos) // blank line + mcp entries
	}
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

	return strings.Join(combined, "\n")
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
// Order: provider/model, then MCP server status.
func (h Header) renderRight(width int) string {
	aligner := lipgloss.NewStyle().Width(width).Align(lipgloss.Right)
	var lines []string

	// Provider/model line
	if h.showBanner && h.banner != "" {
		lines = append(lines, aligner.Render(titleStyle.Render(h.provider+"/"+h.model)))
	} else {
		lines = append(lines, aligner.Render(titleStyle.Render("yaah · "+h.provider+"/"+h.model)))
	}

	// Version line
	if h.version != "" {
		lines = append(lines, aligner.Render(lipgloss.NewStyle().Faint(true).Render(h.version)))
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

	return strings.Join(lines, "\n")
}
