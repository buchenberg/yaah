package modelpicker

import (
	"testing"

	"github.com/rivo/tview"
)

// Escape calls dismiss without calling onSelect, so dismissal must report
// itself or the caller can never restore focus (Esc would stop working
// afterwards). Both the Escape branch and the item-selected branch route
// through this same closure.
func TestShow_DismissNotifiesAndLeavesNoPage(t *testing.T) {
	app := tview.NewApplication()
	pages := tview.NewPages()

	var selected bool
	dismissed := 0
	dismiss := Show(app, pages,
		[]string{"provider/model"}, map[string]string{"provider/model": "Provider"},
		func(string) { selected = true },
		func(string) { dismissed++ },
		nil,
	)

	if got := pages.GetPageCount(); got != 1 {
		t.Fatalf("page count after Show = %d, want 1", got)
	}
	dismiss()

	if dismissed != 1 {
		t.Fatalf("onDismiss called %d times, want exactly 1", dismissed)
	}
	if selected {
		t.Error("onSelect fired on dismissal")
	}
	if got := pages.GetPageCount(); got != 0 {
		t.Fatalf("page count after dismiss = %d, want 0", got)
	}
}
