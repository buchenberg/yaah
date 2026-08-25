package input

import (
	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Prompt is a single-line input row. The TextArea keeps the original
// bordered style with pink border and focus double-border.
type Prompt struct {
	*tview.TextArea
}

// BuildPrompt creates the prompt input. Returns a Prompt whose embedded
// TextArea is the focusable, bordered input field — identical to the
// original Build but with a placeholder that includes the ❯ glyph.
func BuildPrompt(th *colors.Theme) *Prompt {
	area := tview.NewTextArea().
		SetPlaceholder("❯ Type a message… (Enter to send, Ctrl+P commands)").
		SetMaxLength(10000)

	if th != nil && th.NoColor {
		area.SetBorder(true).
			SetBorderColor(tcell.ColorDefault)
		area.SetPlaceholderStyle(tcell.StyleDefault)
		area.SetTextStyle(tcell.StyleDefault)
	} else {
		borderColor := tcell.ColorGray
		if th != nil && th.InputBorder != "" {
			borderColor = tcell.GetColor(th.InputBorder)
		}
		area.SetBorder(true).
			SetBorderColor(borderColor)
		area.SetPlaceholderStyle(tcell.StyleDefault.Foreground(tcell.ColorGray))
		area.SetTextStyle(tcell.StyleDefault.Foreground(tcell.ColorWhite))
	}

	return &Prompt{TextArea: area}
}

// Build creates a bordered TextArea (legacy layout — 3 rows with border).
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
