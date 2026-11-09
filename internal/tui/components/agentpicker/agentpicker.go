// Package agentpicker implements the persona picker modal for the TUI
// (yaah-21uk.10): one entry per primary-capable persona, its
// description as the secondary line. Picking calls back with the
// persona name; the driver then sets the process-level next-session
// override.
package agentpicker

import (
	"github.com/buchenberg/yaah/internal/tui/components/modal"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const modalPageName = "agentpicker_modal"

// Show displays the persona picker. onDismiss runs whenever the picker
// goes away — on selection AND on Escape — mirroring the model picker's
// focus contract: it is the caller's bookkeeping hook, not a "cancelled"
// signal. The returned func dismisses the picker programmatically, which
// is also how tests exercise the Escape path.
func Show(app *tview.Application, pages *tview.Pages, names []string, descriptions map[string]string, onSelect func(name string), onDismiss func()) func() {
	// Idempotent, like the model picker: the Escape capture and the
	// item-selected callback share this closure.
	closed := false
	dismiss := func() {
		if closed {
			return
		}
		closed = true
		pages.RemovePage(modalPageName)
		if onDismiss != nil {
			onDismiss()
		}
	}

	list := tview.NewList().
		ShowSecondaryText(true).
		SetHighlightFullLine(true)
	list.SetMainTextColor(tcell.ColorWhite).
		SetSecondaryTextColor(tcell.ColorGray).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tcell.ColorDarkCyan)

	for _, name := range names {
		persona := name
		list.AddItem(persona, descriptions[persona], 0, func() {
			dismiss()
			if onSelect != nil {
				onSelect(persona)
			}
		})
	}

	list.SetBorder(true).
		SetTitle(" Agent Picker ").
		SetTitleColor(tcell.ColorYellow)

	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			dismiss()
			return nil
		}
		return ev
	})

	pages.AddPage(modalPageName, modal.Wrap(list), true, true)
	app.SetFocus(list)
	return dismiss
}
