// Package messages builds the conversation message area.
package messages

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Build creates the scrollable conversation view.
func Build() *tview.TextView {
	tv := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetWordWrap(true)
	tv.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Conversation ")
	return tv
}
