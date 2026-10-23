package tui

import (
	"github.com/buchenberg/yaah/internal/tui/components/approval"
	"github.com/buchenberg/yaah/internal/tui/components/errdialog"
	"github.com/buchenberg/yaah/internal/tui/components/help"
	"github.com/buchenberg/yaah/internal/tui/components/modelpicker"
	"github.com/buchenberg/yaah/internal/tui/components/question"
)

// Modals — modal dialog wrappers.

// beginModal/endModal bracket every layered view. Global key handling reads
// App.focus to decide whether Escape means "dismiss this layer" or "stop the
// running turn", so each overlay must claim focusNormal while it is up — not
// just the ones that happened to set it before.
func (t *App) beginModal() {
	t.hideSuggestions()
	t.focus = focusModal
}

func (t *App) endModal() {
	t.focus = focusNormal
	t.App.SetFocus(t.Input)
}

// ShowQuestion displays a question modal and returns answers via channel.
func (t *App) ShowQuestion(header, questionText string, opts []struct{ Label, Description string }, multiple bool, onAnswer func(question.Answer)) {
	t.beginModal()
	question.Show(t.App, t.Pages, header, questionText, opts, multiple, func(a question.Answer) {
		t.endModal()
		if onAnswer != nil {
			onAnswer(a)
		}
	})
}

// ShowApproval displays an approval modal and returns via callback.
func (t *App) ShowApproval(name, args string, onAnswer func(bool)) {
	t.beginModal()
	done := func(ok bool) {
		t.endModal()
		if onAnswer != nil {
			onAnswer(ok)
		}
	}
	if t.ShowApprovalFn != nil {
		t.ShowApprovalFn(name, args, done)
		return
	}
	approval.Show(t.App, t.Pages, name, args, done)
}

// ShowModelPicker displays a model picker modal.
func (t *App) ShowModelPicker(models []string, providerNames map[string]string, onSelect func(string)) {
	t.beginModal()
	modelpicker.Show(t.App, t.Pages, models, providerNames, func(model string) {
		t.endModal()
		if onSelect != nil {
			onSelect(model)
		}
	}, t.Input)
}

// ShowHelp displays the help overlay.
func (t *App) ShowHelp() {
	var bindings []help.Binding
	for _, b := range DefaultBindings() {
		bindings = append(bindings, help.Binding{Label: b.Label, HelpText: b.HelpText})
	}
	t.beginModal()
	help.Show(t.App, t.Pages, bindings, t.endModal)
}

// showErrorDialog displays an error dialog using tvxwidgets.MessageDialog.
// The conversation log line (if any) should be appended separately — the
// dialog is the user-visible notification, the log line is the durable
// record.
func (t *App) showErrorDialog(title, msg string) {
	t.beginModal()
	errdialog.Show(t.App, t.Pages, title, msg, t.endModal)
}
