package infopane

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Build creates an empty right-side info pane with a subtle dark
// background. Its content is populated at runtime from live
// control-channel data by TUI2.renderInfoPane.
func Build() *tview.TextView {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(true)
	tv.SetBackgroundColor(tcell.Color236) // dark gray-blue (#303030)
	return tv
}
