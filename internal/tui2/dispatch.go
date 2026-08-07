package tui2

import (
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/components/approval"
	"github.com/buchenberg/yaah/internal/tui2/components/command"
	"github.com/buchenberg/yaah/internal/tui2/components/help"
	"github.com/buchenberg/yaah/internal/tui2/components/modelpicker"
	"github.com/buchenberg/yaah/internal/tui2/components/question"
	"github.com/buchenberg/yaah/internal/tui2/components/todo"
	"github.com/gdamore/tcell/v2"

	itodo "github.com/buchenberg/yaah/internal/todo"
)

// globalInputCapture handles global keybindings (before tview routing).
func (t *TUI2) globalInputCapture(ev *tcell.EventKey) *tcell.EventKey {
	action := Translate(ev, DefaultBindings())
	switch action {
	case ActionQuit:
		t.Stop()
		return nil
	case ActionHelp:
		help.Show(t.App, t.Pages, bindingsToHelpBindings(DefaultBindings()))
		return nil
	case ActionCommand:
		const cmdModal = "cmdpalette_modal"
		if t.Pages.HasPage(cmdModal) {
			t.Pages.RemovePage(cmdModal)
			t.App.SetFocus(t.Input)
		} else {
			t.CmdPalette.SetText("")
			t.Pages.AddPage(cmdModal, t.CmdPalette, true, true)
			t.App.SetFocus(t.CmdPalette)
		}
		return nil
	case ActionClear:
		t.conversation.Clear()
		t.refreshMessages()
		return nil
	case ActionToggleReasoning:
		for _, rb := range t.reasoningBlocks {
			rb.Toggle()
		}
		t.refreshMessages()
		return nil
	case ActionToggleTools:
		for _, tb := range t.toolBlocks {
			tb.Toggle()
		}
		t.refreshMessages()
		return nil
	case ActionToggleSubAgents:
		for _, sb := range t.subagentBlocks {
			sb.Toggle()
		}
		t.refreshMessages()
		return nil
	case ActionSend:
		if t.App.GetFocus() != t.Input {
			return ev
		}
		if t.OnSubmit != nil {
			text := t.Input.GetText()
			if strings.TrimSpace(text) != "" {
				t.Input.SetText("", false)
				t.OnSubmit(text)
			}
		}
		return nil
	case ActionCancel:
		if t.App.GetFocus() != t.Input {
			return ev
		}
		if t.OnAbort != nil {
			t.OnAbort()
		}
		return nil
	}
	return ev
}

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
	modelpicker.Show(t.App, t.Pages, models, providerNames, onSelect)
}

// HandleCommand dispatches a colon command.
func (t *TUI2) HandleCommand(cmd command.Cmd, arg string) {
	switch cmd {
	case command.CmdQuit:
		t.Stop()
	case command.CmdClear:
		t.conversation.Clear()
		t.refreshMessages()
	case command.CmdHelp:
		help.Show(t.App, t.Pages, bindingsToHelpBindings(DefaultBindings()))
	case command.CmdCompact:
		t.CollapseAll()
	case command.CmdModel:
		t.ShowModelPicker(t.availableModels, t.providerNames, func(model string) {
			if t.OnModelSelect != nil {
				t.OnModelSelect(model)
			}
		})
	}
}

// UpdateTodos updates the TODO list in the right panel.
func (t *TUI2) UpdateTodos(items []itodo.Item) {
	t.TodoPane.SetText(todo.FormatList(items))
}

// UpdateInfopane updates the info pane content (single-pane, not tabbed yet).
func (t *TUI2) UpdateInfopane(content string) {
	t.InfoPane.SetText(content)
}

// bindingsToHelpBindings converts internal bindings to the help package's format.
func bindingsToHelpBindings(bindings []Binding) []help.Binding {
	var out []help.Binding
	for _, b := range bindings {
		out = append(out, help.Binding{Label: b.Label, HelpText: b.HelpText})
	}
	return out
}
