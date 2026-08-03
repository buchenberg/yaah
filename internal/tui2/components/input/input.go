// Package input builds the multi-line input area.
package input

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Build creates the multi-line text input area.
func Build() *tview.TextArea {
	ta := tview.NewTextArea().
		SetPlaceholder("Type your message... (Ctrl+S to send, Ctrl+D to quit)").
		SetMaxLength(10000)
	ta.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Input ")

	ta.SetPlaceholderStyle(
		tcell.StyleDefault.Foreground(tcell.ColorGray),
	)
	ta.SetTextStyle(
		tcell.StyleDefault.Foreground(tcell.ColorWhite),
	)
	return ta
}
