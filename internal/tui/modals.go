package tui

import (
	"github.com/buchenberg/yaah/internal/tui/components/approval"
	"github.com/buchenberg/yaah/internal/tui/components/errdialog"
	"github.com/buchenberg/yaah/internal/tui/components/help"
	"github.com/buchenberg/yaah/internal/tui/components/modelpicker"
	"github.com/buchenberg/yaah/internal/tui/components/question"
)

// Modals — modal dialog wrappers.

// ShowQuestion displays a question modal and returns answers via channel.
func (t *App) ShowQuestion(header, questionText string, opts []struct{ Label, Description string }, multiple bool, onAnswer func(question.Answer)) {
	question.Show(t.App, t.Pages, header, questionText, opts, multiple, onAnswer)
}

// ShowApproval displays an approval modal and returns via callback.
func (t *App) ShowApproval(name, args string, onAnswer func(bool)) {
	if t.ShowApprovalFn != nil {
		t.ShowApprovalFn(name, args, onAnswer)
		return
	}
	approval.Show(t.App, t.Pages, name, args, onAnswer)
}

// ShowModelPicker displays a model picker modal.
func (t *App) ShowModelPicker(models []string, providerNames map[string]string, onSelect func(string)) {
	modelpicker.Show(t.App, t.Pages, models, providerNames, onSelect, t.Input)
}

// ShowHelp displays the help overlay.
func (t *App) ShowHelp() {
	var bindings []help.Binding
	for _, b := range DefaultBindings() {
		bindings = append(bindings, help.Binding{Label: b.Label, HelpText: b.HelpText})
	}
	help.Show(t.App, t.Pages, bindings, func() {
		t.App.SetFocus(t.Input)
		t.focus = focusNormal
	})
}

// showErrorDialog displays an error dialog using tvxwidgets.MessageDialog.
// The conversation log line (if any) should be appended separately — the
// dialog is the user-visible notification, the log line is the durable
// record.
func (t *App) showErrorDialog(title, msg string) {
	t.focus = focusModal
	errdialog.Show(t.App, t.Pages, title, msg, func() {
		t.focus = focusNormal
		t.App.SetFocus(t.Input)
	})
}
