// Package modal provides a common modal wrapper for consistent sizing and
// centering across all TUI overlays.
package modal

import "github.com/rivo/tview"

// Wrap centers a primitive inside a flex with proportional padding, giving
// a consistently sized modal overlay regardless of terminal dimensions.
// The content occupies roughly 3/5 of both width and height.
func Wrap(content tview.Primitive) *tview.Flex {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(content, 0, 3, false).
			AddItem(nil, 0, 1, false), 0, 3, true).
		AddItem(nil, 0, 1, false)
}
