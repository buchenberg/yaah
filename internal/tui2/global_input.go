package tui2

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

// globalInputCapture handles top-level key events before they reach the focused primitive.
func (t *TUI2) globalInputCapture(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyCtrlD:
		t.Stop()
		return nil
	case tcell.KeyCtrlS:
		t.pressSend()
		return nil
	}
	return event
}

// pressSend simulates the send action from the input area.
func (t *TUI2) pressSend() {
	text := strings.TrimSpace(t.Input.GetText())
	if text != "" {
		t.addUserMessage(text)
		t.clearInput()
	}
}

// clearInput resets the input area and refocuses it.
func (t *TUI2) clearInput() {
	t.Input.SetText("", true)
	t.App.SetFocus(t.Input)
}
