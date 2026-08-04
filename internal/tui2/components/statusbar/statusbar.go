package statusbar

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func Build() (*tview.TextView, string) {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignRight).
		SetDynamicColors(true).
		SetWordWrap(false)
	tv.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Status ")

	return tv, ""
}

func Update(tv *tview.TextView, provider, model string, contextTokens, contextWindow int) {
	var b strings.Builder
	if provider != "" {
		b.WriteString(fmt.Sprintf("%s  ", colors.Tag(colors.Dim, provider)))
	}
	if model != "" {
		b.WriteString(fmt.Sprintf("%s  ", colors.Tag(colors.Dim, model)))
	}
	if contextWindow > 0 {
		b.WriteString(fmt.Sprintf("Context: %s  ",
			colors.Tag(colors.Dim, fmt.Sprintf("%d/%d", contextTokens, contextWindow)),
		))
	}
	tv.SetText(b.String())
}
