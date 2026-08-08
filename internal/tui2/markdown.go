package tui2

import (
	"github.com/buchenberg/tviewmd"
	"github.com/rivo/tview"
)

// renderMarkdown converts markdown to tview color-tagged text using the
// tviewmd native renderer (goldmark parser + tview tag backend). tview's
// SetWrap(true) + SetWordWrap(true) on the Messages TextView handles all
// line wrapping; tviewmd only uses Width for table column math and
// horizontal-rule length.
func renderMarkdown(md string) string {
	if md == "" {
		return ""
	}
	return tviewmd.Render(md, tviewmd.Options{})
}

// messageWidth returns a usable width for component rendering from the
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
