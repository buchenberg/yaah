package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"
)

// ExpandableSection renders a collapsible content block behind a
// zone-marked toggle header. Collapsed shows "▶ header"; expanded shows
// "▼ header" followed by the wrapped content on a background style.
// Used for reasoning sections and other toggleable blocks.
type ExpandableSection struct {
	zoneID   string
	header   string
	expanded bool
	content  string
	width    int
	bgStyle  lipgloss.Style
	fgStyle  lipgloss.Style

	// preWrapped marks content that is already formatted and
	// width-constrained (e.g. glamour-rendered markdown). Pre-wrapped
	// content is rendered as-is; chatWrap would destroy code-block
	// indentation and split ANSI escape runs.
	preWrapped bool
}

// NewExpandableSection creates an expandable section component.
func NewExpandableSection(zoneID, header string, expanded bool, content string, width int, bgStyle, fgStyle lipgloss.Style) ExpandableSection {
	return ExpandableSection{
		zoneID:   zoneID,
		header:   header,
		expanded: expanded,
		content:  content,
		width:    width,
		bgStyle:  bgStyle,
		fgStyle:  fgStyle,
	}
}

// AsPreWrapped marks the section's content as pre-formatted and
// width-constrained, skipping chatWrap during render.
func (e ExpandableSection) AsPreWrapped() ExpandableSection {
	e.preWrapped = true
	return e
}

// Render returns the zone-marked toggle header, and when expanded,
// the wrapped content beneath it. When the header text contains ANSI
// escape sequences (e.g. lolcat-styled), toggleStyle is skipped so the
// pre-rendered colors are preserved.
func (e ExpandableSection) Render() string {
	var b strings.Builder
	hdrStyle := toggleStyle
	if strings.Contains(e.header, "\x1b[") {
		hdrStyle = lipgloss.NewStyle()
	}
	if !e.expanded {
		b.WriteString(zone.Mark(e.zoneID, hdrStyle.Render("  ▶ "+e.header)))
		b.WriteString("\n")
		return b.String()
	}
	b.WriteString(zone.Mark(e.zoneID, hdrStyle.Render("  ▼ "+e.header)))
	b.WriteString("\n\n")
	if e.preWrapped {
		b.WriteString(e.bgStyle.Width(e.width).PaddingLeft(4).Render(e.fgStyle.Render(e.content)))
	} else {
		b.WriteString(chatBubble(e.content, e.width, e.fgStyle, e.bgStyle))
	}
	b.WriteString("\n")
	return b.String()
}
