package infobar

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func Build() (*tview.TextView, string) {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(false)
	tv.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Info ")

	return tv, ""
}
