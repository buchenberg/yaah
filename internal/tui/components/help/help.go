// Package help renders the help overlay showing keybindings and commands.
//
// Triggered by "?" or ":help".
package help

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui/components/modal"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Binding is a keybinding entry for the help overlay.
type Binding struct {
	Label    string
	HelpText string
}

const modalPageName = "help_modal"

// Show displays the help overlay with keybindings and command reference.
// onDismiss is called when the overlay is dismissed (Escape or Enter).
func Show(app *tview.Application, pages *tview.Pages, bindings []Binding, onDismiss func()) {
	var lines []string
	lines = append(lines, "[yellow]Keyboard Shortcuts[-]\n\n")
	for _, b := range bindings {
		lines = append(lines, fmt.Sprintf("  [white]%-12s[-] [dim]%s[-]", b.Label, b.HelpText))
	}
	lines = append(lines, "\n[yellow]Commands (Ctrl+P → :command)[-]\n")
	lines = append(lines, "  :help          Show this help")
	lines = append(lines, "  :clear         Clear conversation")
	lines = append(lines, "  :compact       Compact context")
	lines = append(lines, "  :stop          Stop running agent")
	lines = append(lines, "  :steer <text>  Inject steering text")
	lines = append(lines, "  :model <name>  Switch model")
	lines = append(lines, "  :search <q>    Search messages")
	lines = append(lines, "  :verbose       Toggle verbose mode")
	lines = append(lines, "  :banner        Toggle banner")
	lines = append(lines, "  :top/:bottom   Scroll to top/bottom")
	lines = append(lines, "  :quit          Exit")

	textView := tview.NewTextView().
		SetDynamicColors(true).
		SetText(strings.Join(lines, "\n"))
	textView.SetBorder(true).
		SetTitle(" Help ").
		SetTitleColor(tcell.ColorYellow)

	flex := modal.Wrap(textView)

	flex.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape || ev.Key() == tcell.KeyEnter {
			pages.RemovePage(modalPageName)
			onDismiss()
			return nil
		}
		return ev
	})

	pages.AddPage(modalPageName, flex, true, true)
	app.SetFocus(textView)
}
