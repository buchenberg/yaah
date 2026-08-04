// Package input builds the multi-line input area.
package input

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Build creates the multi-line text input area.
func Build() *tview.TextArea {
	ta := tview.NewTextArea().
		SetPlaceholder("Type your message... (Enter to send, Ctrl+P for commands, Ctrl+C to quit)").
		SetMaxLength(10000)
	ta.SetBorder(true).
		SetBorderColor(tcell.ColorGray)

	ta.SetPlaceholderStyle(
		tcell.StyleDefault.Foreground(tcell.ColorGray),
	)
	ta.SetTextStyle(
		tcell.StyleDefault.Foreground(tcell.ColorWhite),
	)
	return ta
}
