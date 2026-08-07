package tui2

import (
	"github.com/buchenberg/tviewmd"
	"github.com/rivo/tview"
)

// renderMarkdown converts markdown to tview color-tagged text using the
// tviewmd native renderer (goldmark parser + tview tag backend). The
// output is suitable for a tview.TextView with SetWrap(true).
func renderMarkdown(md string, width int) string {
	if md == "" {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	return tviewmd.Render(md, tviewmd.Options{Width: width})
}

// messageWidth returns a usable width for markdown rendering from the
// messages pane, defaulting to 80 if the pane hasn't been laid out yet.
func messageWidth(tv *tview.TextView) int {
	if tv == nil {
		return 80
	}
	_, _, w, _ := tv.GetInnerRect()
	if w <= 0 {
		return 80
	}
	return w
}
