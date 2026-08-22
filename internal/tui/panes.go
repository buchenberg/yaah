package tui

import (
	"time"
)

// Panes — right-pane update methods.

// SetEphemeral shows a transient status message in the info pane for 3
// seconds, then clears it.
func (t *App) SetEphemeral(msg string) {
	t.ephemeralMsg = msg
	t.renderInfoPane()
	go func() {
		time.Sleep(3 * time.Second)
		go t.App.QueueUpdateDraw(func() {
			t.ephemeralMsg = ""
			t.renderInfoPane()
		})
	}()
}
