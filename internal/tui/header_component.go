package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// Header renders the top-of-screen title block: the ASCII art banner
// plus provider/model line with keybinding hints on the right side.
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

// Render returns the header block with keybinding hints on the right.
func (h Header) Render() string {
	keyHints := commandDescStyle.Render(": commands  / search  ? help  ctrl+y copy  ctrl+c quit")

	if h.showBanner && h.banner != "" {
		return h.banner + "\n\n" +
			h.renderLine(titleStyle.Render(fmt.Sprintf("%s/%s", h.provider, h.model)), keyHints) + "\n"
	}
	return h.renderLine(
		titleStyle.Render(fmt.Sprintf("yaah · %s/%s", h.provider, h.model)),
		keyHints,
	) + "\n\n"
}

// renderLine creates a full-width line with left content and right-aligned hints.
func (h Header) renderLine(left, right string) string {
	if h.width <= 0 {
		return left + "  " + right
	}
	rightAligned := lipgloss.NewStyle().Width(h.width - lipgloss.Width(left)).Align(lipgloss.Right).Render(right)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, rightAligned)
}
