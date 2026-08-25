// Package errdialog displays error messages using tvxwidgets.MessageDialog.
package errdialog

import (
	"strings"

	"github.com/navidys/tvxwidgets"
	"github.com/rivo/tview"
)

const pageName = "error_dialog"

const (
	maxLines = 30
	maxChars = 1200
)

// Show displays a tvxwidgets error dialog over the given Pages. The
// dialog is removed when the user presses Enter or Esc. onDone is
// called after removal (use it to restore focus).
//
// Note the upstream misspelling: tvxwidgets.ErrorDailog (not ErrorDialog).
func Show(app *tview.Application, pages *tview.Pages, title, msg string, onDone func()) {
	d := tvxwidgets.NewMessageDialog(tvxwidgets.ErrorDailog)
	d.SetTitle(title)
	d.SetMessage(clip(msg))
	d.SetDoneFunc(func() {
		pages.RemovePage(pageName)
		if onDone != nil {
			onDone()
		}
	})
	pages.AddPage(pageName, d, false, true)
	pages.SendToFront(pageName)
	app.SetFocus(d)
}

// clip truncates msg to maxLines lines and maxChars runes so the dialog
// fits in a reasonable terminal.
func clip(msg string) string {
	lines := strings.Split(msg, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, "…(truncated)")
	}
	result := strings.Join(lines, "\n")
	runes := []rune(result)
	if len(runes) > maxChars {
		result = string(runes[:maxChars]) + "…"
	}
	return result
}
