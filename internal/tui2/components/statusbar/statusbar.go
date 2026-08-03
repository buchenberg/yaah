// Package statusbar builds the bottom status bar.
package statusbar

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Build creates the status bar and returns a format string for later updates.
func Build() (*tview.TextView, string) {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignRight).
		SetDynamicColors(true).
		SetWordWrap(false)
	tv.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Status ")

	statusInfo := fmt.Sprintf("%s  %s  %s\n",
		colors.Tag(colors.Dim, "Context: 12.4K/128K"),
		colors.Tag(colors.Dim, "Model: gpt-4o"),
		colors.Tag(colors.Dim, "$0.042"),
	)
	tv.SetText(statusInfo)
	return tv, statusInfo
}
