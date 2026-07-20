package tui

import "charm.land/lipgloss/v2"

// Component is the minimal interface for TUI render elements.
// Components are constructed per render and discarded — they are
// stateless renderers, not long-lived widgets.
type Component interface {
	Render() string
}

// BaseComponent holds the shared rendering properties for simple
// content components: width constraint and a style.
type BaseComponent struct {
	content string
	width   int
	style   lipgloss.Style
}

// NewBaseComponent creates a component that renders content through
// the given style at the given width. A zero width renders without
// a width constraint.
func NewBaseComponent(content string, width int, style lipgloss.Style) BaseComponent {
	return BaseComponent{content: content, width: width, style: style}
}

// Render returns the styled, width-constrained content.
func (c BaseComponent) Render() string {
	if c.width > 0 {
		return c.style.Width(c.width).Render(c.content)
	}
	return c.style.Render(c.content)
}

// chatBubble renders content word-wrapped to width in the foreground
// style, on a width-constrained background. It is the shared shape of
// user messages, system messages, and expandable-section content.
func chatBubble(content string, width int, fg, bg lipgloss.Style) string {
	return bg.Width(width).Render(fg.Render(chatWrap("", content, width)))
}

// scrollWindow computes the visible [start, end) window over total
// items, centered on selected and capped at maxVisible, clamped to
// [0, total]. Shared by all scrollable palettes.
func scrollWindow(selected, maxVisible, total int) (start, end int) {
	start = selected - maxVisible/2
	if start < 0 {
		start = 0
	}
	end = start + maxVisible
	if end > total {
		end = total
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}
	return start, end
}
