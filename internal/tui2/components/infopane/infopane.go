package infopane

import (
	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func Build(th *colors.Theme) *tview.TextView {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(true)
	tv.SetBorder(true)
	tv.SetTitle(" Info ")
	if th.PaneBorder != "" {
		tv.SetBorderColor(tcell.GetColor(th.PaneBorder))
		tv.SetTitleColor(tcell.GetColor(th.PaneBorder))
	}
	return tv
}
