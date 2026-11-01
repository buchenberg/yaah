package tui

import (
	"github.com/gdamore/tcell/v2"
)

// globalInputCapture handles global keybindings (before tview routing).
func (t *App) globalInputCapture(ev *tcell.EventKey) *tcell.EventKey {
	// Typeahead owns Tab, Up and Down while its strip is visible over the
	// composer, so those keys complete and cycle instead of editing or scrolling.
	// Only when the composer actually holds the keys: Tab and the arrows belong
	// to whatever pane has focus, and typeahead must not capture them from there.
	if t.hasSuggestions() && t.focus == focusNormal && t.App.GetFocus() == t.Input {
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
		// Gate on the run, not on the token stream. isStreaming is only true
		// between the first content token and the next flush, so it is false
		// while the model is thinking, while tools run, and between
		// iterations. Treating those windows as idle started a second
		// concurrent RunPrompt on the same session, whose stale message
		// snapshot lost the turn in flight and wedged message persistence.
		if t.agentActive {
			t.submitFollowUp()
		} else {
			t.submitInput()
		}
		return nil
	case ActionCancel:
		// A layered view owns Escape: the palette, dialogs and pickers dismiss
		// themselves with it, and stopping the turn underneath them would be a
		// surprise. Every overlay claims focusModal via beginModal, so this check
		// must come first — including before the typeahead strip, which would
		// otherwise eat the Escape meant for the layer above it.
		if t.focus != focusNormal {
			return ev
		}
		// Typeahead is next: while the strip is open the user is editing a
		// command name, not asking to stop the turn.
		if t.dismissSuggestions() {
			return nil
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
