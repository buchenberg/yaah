package tui2

import "github.com/rivo/tview"

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
