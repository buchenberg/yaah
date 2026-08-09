package tui2

import (
	"github.com/buchenberg/yaah/internal/tui2/components/approval"
	"github.com/buchenberg/yaah/internal/tui2/components/help"
	"github.com/buchenberg/yaah/internal/tui2/components/modelpicker"
	"github.com/buchenberg/yaah/internal/tui2/components/question"
)

// Modals — modal dialog wrappers.

// ShowQuestion displays a question modal and returns answers via channel.
func (t *TUI2) ShowQuestion(header, questionText string, opts []struct{ Label, Description string }, multiple bool, onAnswer func(question.Answer)) {
	question.Show(t.App, t.Pages, header, questionText, opts, multiple, onAnswer)
}

// ShowApproval displays an approval modal and returns via callback.
func (t *TUI2) ShowApproval(name, args string, onAnswer func(bool)) {
	approval.Show(t.App, t.Pages, name, args, onAnswer)
}

// ShowModelPicker displays a model picker modal.
func (t *TUI2) ShowModelPicker(models []string, providerNames map[string]string, onSelect func(string)) {
	modelpicker.Show(t.App, t.Pages, models, providerNames, onSelect, t.Input)
}

// ShowHelp displays the help overlay.
func (t *TUI2) ShowHelp() {
	var bindings []help.Binding
	for _, b := range DefaultBindings() {
		bindings = append(bindings, help.Binding{Label: b.Label, HelpText: b.HelpText})
	}
	help.Show(t.App, t.Pages, bindings, func() {
		t.App.SetFocus(t.Input)
		t.focus = focusNormal
	})
}
