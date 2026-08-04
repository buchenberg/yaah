// Package help renders the help overlay showing all keybindings.
//
// Triggered by "?" or ":help".
package help

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Binding is a simplified keybinding entry for the help overlay.
type Binding struct {
	Label    string
	HelpText string
}

const modalPageName = "help_modal"

// Show displays the help overlay.
func Show(app *tview.Application, pages *tview.Pages, bindings []Binding) {
	// Group by category.
	var lines []string
	lines = append(lines, "[yellow]Keyboard Shortcuts[-]\n")

	maxLabel := 0
	for _, b := range bindings {
		if len(b.Label) > maxLabel {
			maxLabel = len(b.Label)
		}
	}

	for _, b := range bindings {
		pad := strings.Repeat(" ", maxLabel-len(b.Label)+2)
		lines = append(lines, fmt.Sprintf("  [white]%s[-]%s[dim]%s[-]", b.Label, pad, b.HelpText))
	}

	text := strings.Join(lines, "\n")

	textView := tview.NewTextView().
		SetDynamicColors(true).
		SetText(text)

	textView.SetBorder(true).
		SetTitle(" Help ").
		SetTitleColor(tcell.ColorYellow)

	// Wrap in a flex for padding.
	flex := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(textView, 0, 3, false).
			AddItem(nil, 0, 1, false), 0, 3, true).
		AddItem(nil, 0, 1, false)

	flex.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape || (ev.Key() == tcell.KeyRune && ev.Rune() == '?') {
			pages.RemovePage(modalPageName)
			return nil
		}
		return ev
	})

	pages.AddPage(modalPageName, flex, true, true)
}
