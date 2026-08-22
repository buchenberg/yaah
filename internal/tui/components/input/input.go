package input

import (
	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func Build(th *colors.Theme) *tview.TextArea {
	ta := tview.NewTextArea().
		SetPlaceholder("Type your message... (Enter to send, Ctrl+P for commands, Ctrl+C to quit)").
		SetMaxLength(10000)

	if th != nil && th.NoColor {
		ta.SetBorder(true).
			SetBorderColor(tcell.ColorDefault)
		ta.SetPlaceholderStyle(tcell.StyleDefault)
		ta.SetTextStyle(tcell.StyleDefault)
	} else {
		borderColor := tcell.ColorGray
		if th != nil && th.InputBorder != "" {
			borderColor = tcell.GetColor(th.InputBorder)
		}
		ta.SetBorder(true).
			SetBorderColor(borderColor)
		ta.SetPlaceholderStyle(tcell.StyleDefault.Foreground(tcell.ColorGray))
		ta.SetTextStyle(tcell.StyleDefault.Foreground(tcell.ColorWhite))
	}
	return ta
}
