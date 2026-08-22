package tui

import (
	"github.com/gdamore/tcell/v2"
)

// globalInputCapture handles global keybindings (before tview routing).
func (t *App) globalInputCapture(ev *tcell.EventKey) *tcell.EventKey {
	action := Translate(ev, DefaultBindings())
	switch action {
	case ActionQuit:
		t.Stop()
		return nil
	case ActionCommand:
		t.toggleCommandPalette()
		return nil
	case ActionClear:
		t.doClear()
		return nil
	case ActionToggleReasoning:
		t.toggleAllReasoning()
		return nil
	case ActionToggleTools:
		t.toggleAllTools()
		return nil
	case ActionToggleSubAgents:
		t.toggleAllSubAgents()
		return nil
	case ActionSend:
		if t.App.GetFocus() != t.Input {
			return ev
		}
		if t.isStreaming.Load() {
			t.submitFollowUp()
		} else {
			t.submitInput()
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
	case ActionScrollUp, ActionPageUp, ActionTop:
		t.userScrolled = true
	case ActionScrollDown, ActionPageDown, ActionBottom:
		t.userScrolled = false
	}
	return ev
}
