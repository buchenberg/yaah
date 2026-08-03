// Package separator builds the dim rule line that sits between the header
// content and the body.
package separator

import (
	"github.com/rivo/tview"
)

// Build creates the dim separator TextView ("─── agent harness · vendor-free ──").
func Build() *tview.TextView {
	return tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft).
		SetText("[#5f5f5f::d]─── agent harness · vendor-free ────────────────────────────────[-]")
}
