// Package approval renders interactive approval modals for tool calls.
//
// Dispatched from CtrlApproval events, the modal shows the tool name,
// arguments, and Yes/No buttons. The result is returned via a callback.
package approval

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const modalPageName = "approval_modal"

// Show displays the approval modal for a tool call.
//
//	name  — tool name (e.g. "bash")
//	args  — tool arguments (e.g. "rm -rf /")
//	onAnswer — called with true (approved) or false (denied)
func Show(app *tview.Application, pages *tview.Pages, name, args string, onAnswer func(bool)) {
	text := fmt.Sprintf("[yellow]Approve tool call?[-]\n\n"+
		"[white]Tool:[-] %s\n"+
		"[white]Args:[-] %s\n\n"+
		"[dim]Enter: Approve  Esc: Deny[-]", name, args)

	modal := tview.NewModal().
		SetText(text).
		AddButtons([]string{"Yes", "No"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			pages.RemovePage(modalPageName)
			if onAnswer != nil {
				onAnswer(buttonLabel == "Yes")
			}
		})

	modal.SetBorder(true).
		SetTitle(" Approve ").
		SetTitleColor(tcell.ColorYellow).
		SetBackgroundColor(tcell.ColorDefault)

	pages.AddPage(modalPageName, modal, false, true)
	app.SetFocus(modal)
}
