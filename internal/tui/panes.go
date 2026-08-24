package tui

import (
	"time"
)

// Panes — right-pane update methods.

// ephemeralTTL is how long an ephemeral message stays visible. A var so
// tests can shrink it.
var ephemeralTTL = 3 * time.Second

// SetEphemeral shows a transient status message in the info pane, then
// clears it after ephemeralTTL. A generation counter ensures a stale
// timer never clears a newer message: only the most recent call's
// goroutine performs the clear.
func (t *App) SetEphemeral(msg string) {
	t.ephemeralMsg = msg
	t.renderInfoPane()

	gen := t.ephemeralGen.Add(1)
	ttl := ephemeralTTL // capture before the goroutine starts
	go func() {
		time.Sleep(ttl)
		t.queueUpdateDraw(func() {
			if t.ephemeralGen.Load() != gen {
				return // superseded by a newer SetEphemeral
			}
			t.ephemeralMsg = ""
			t.renderInfoPane()
		})
	}()
}
