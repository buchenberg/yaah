// Package provider builds the provider/model/agent info panel.
package provider

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Build creates the provider info panel.
func Build() *tview.TextView {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignRight).
		SetDynamicColors(true).
		SetWordWrap(false)
	tv.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Provider ")

	tv.SetText(
		fmt.Sprintf("Provider: %s\nModel: %s\nAgent: yaah v0.7.0",
			colors.TagBold(colors.Accent, "openai/gpt-4o"),
			colors.TagBold(colors.Accent, "gpt-4o-2024-08-06"),
		),
	)
	return tv
}
