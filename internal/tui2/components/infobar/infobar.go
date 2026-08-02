// Package infobar builds the single-row info / plan bar.
package infobar

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Build creates the info bar and returns a format string for later updates.
func Build() (*tview.TextView, string) {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(false)
	tv.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Info ")

	planInfo := fmt.Sprintf("%s ⌛ %s %s\n",
		colors.TagBold(colors.Accent, "━━"),
		strings.Repeat("─", 40),
		colors.Tag(colors.Dim, "[no plan loaded]"),
	)

	tv.SetText(planInfo)
	return tv, planInfo
}
