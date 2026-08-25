package input

import (
	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Prompt is a single-line input row: a glyph prefix plus a TextArea.
// The Flex is 1 row tall; multi-line content scrolls inside the
// TextArea without growing the layout.
type Prompt struct {
	*tview.Flex
	Area *tview.TextArea
}

// BuildPrompt creates the single-line prompt row. t.Input should point
// at Prompt.Area so existing submitInput/doClear/focus code is
// unchanged.
func BuildPrompt(th *colors.Theme) *Prompt {
	glyph := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(false)
	if th != nil && !th.NoColor {
		glyph.SetText("[" + th.User + "]❯[-] ")
	} else {
		glyph.SetText("❯ ")
	}
	glyph.SetBackgroundColor(tcell.ColorDefault)

	area := tview.NewTextArea().
		SetPlaceholder("Type a message… (Enter to send, Ctrl+P commands)").
		SetMaxLength(10000)

	if th != nil && th.NoColor {
		area.SetPlaceholderStyle(tcell.StyleDefault)
		area.SetTextStyle(tcell.StyleDefault)
	} else {
		area.SetPlaceholderStyle(tcell.StyleDefault.Foreground(tcell.ColorGray))
		area.SetTextStyle(tcell.StyleDefault.Foreground(tcell.ColorWhite))
	}
	area.SetBackgroundColor(tcell.ColorDefault)

	flex := tview.NewFlex().
		AddItem(glyph, 2, 0, false).
		AddItem(area, 0, 1, false)
	flex.SetBackgroundColor(tcell.ColorDefault)

	return &Prompt{Flex: flex, Area: area}
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
