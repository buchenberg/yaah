// Package input builds the multi-line input area.
package input

import (
	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func Build(th *colors.Theme) *tview.TextArea {
	borderColor := tcell.ColorGray
	if th != nil && th.InputBorder != "" {
		borderColor = tcell.GetColor(th.InputBorder)
	}
	ta := tview.NewTextArea().
		SetPlaceholder("Type your message... (Enter to send, Ctrl+P for commands, Ctrl+C to quit)").
		SetMaxLength(10000)
	ta.SetBorder(true).
		SetBorderColor(borderColor)

	ta.SetPlaceholderStyle(
		tcell.StyleDefault.Foreground(tcell.ColorGray),
	)
	ta.SetTextStyle(
		tcell.StyleDefault.Foreground(tcell.ColorWhite),
	)
	return ta
}
