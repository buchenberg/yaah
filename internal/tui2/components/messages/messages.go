// Package messages builds the conversation message area.
package messages

import (
	"github.com/rivo/tview"
)

// Build creates the scrollable conversation view.
func Build() *tview.TextView {
	return tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetWordWrap(true)
}
