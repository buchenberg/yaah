package infopane

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func Build() *tview.TextView {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(true)
	tv.SetBackgroundColor(tcell.ColorDefault)
	return tv
}
