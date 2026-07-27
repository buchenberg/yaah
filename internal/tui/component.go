package tui

import "charm.land/lipgloss/v2"

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
