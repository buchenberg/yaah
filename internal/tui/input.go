package tui

import (
	"github.com/gdamore/tcell/v2"
)

// globalInputCapture handles global keybindings (before tview routing).
func (t *App) globalInputCapture(ev *tcell.EventKey) *tcell.EventKey {
	// Typeahead owns Tab, Up and Down while its strip is visible over the
	// composer, so those keys complete and cycle instead of editing or scrolling.
	if t.hasSuggestions() && t.focus == focusNormal {
		switch ev.Key() {
		case tcell.KeyTab:
			if t.acceptSuggestion() {
				return nil
			}
		case tcell.KeyUp:
			t.cycleSuggestion(-1)
			return nil
		case tcell.KeyDown:
			t.cycleSuggestion(1)
			return nil
		}
	}
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
		// Escape dismisses the typeahead strip first: while it is open the user
		// is editing a command name, not asking to stop the turn.
		if t.dismissSuggestions() {
			return nil
		}
		// Escape belongs to whatever is layered on top: the command palette and
		// modals dismiss themselves with it. Only at the normal focus does it
		// mean "stop the running turn", and that now holds whether or not the
		// input box happens to be focused.
		if t.focus != focusNormal {
			return ev
		}
		if t.OnStop != nil {
			t.OnStop()
			return nil
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
