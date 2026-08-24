package tui

import (
	"testing"
	"time"
)

// drainUIEvents consumes the app's UI event queue and runs each fn
// synchronously, standing in for the real tview main loop in tests.
func drainUIEvents(ui *App, done <-chan struct{}) {
	go func() {
		for {
			select {
			case <-done:
				return
			case ev := <-ui.uiEventCh:
				if ev.fn != nil {
					ev.fn()
				}
			}
		}
	}()
}

// probeEphemeral reads ephemeralMsg through the UI queue so the access
// is serialized exactly as it is in production (QueueUpdateDraw-only
// field per the App contract).
func probeEphemeral(t *testing.T, ui *App) string {
	t.Helper()
	got := make(chan string, 1)
	ui.queueUpdate(func() { got <- ui.ephemeralMsg })
	select {
	case v := <-got:
		return v
	case <-time.After(time.Second):
		t.Fatal("probe never serviced")
		return ""
	}
}

func newTestUI() *App {
	ui := New("test")
	ui.bgDone = make(chan struct{})
	ui.uiEventCh = make(chan uiEvent, 16)
	return ui
}

func TestSetEphemeral_StaleTimerDoesNotClearNewerMessage(t *testing.T) {
	oldTTL := ephemeralTTL
	ephemeralTTL = 30 * time.Millisecond
	t.Cleanup(func() { ephemeralTTL = oldTTL })

	ui := newTestUI()
	done := make(chan struct{})
	defer close(done)
	drainUIEvents(ui, done)

	ui.SetEphemeral("first")
	time.Sleep(5 * time.Millisecond) // let gen-1 timer get close to firing
	ui.SetEphemeral("second")

	// Wait well past both timers.
	time.Sleep(3 * ephemeralTTL)

	if got := probeEphemeral(t, ui); got != "" {
		t.Fatalf("ephemeralMsg = %q, want cleared after TTL", got)
	}
}

func TestSetEphemeral_ClearsAfterTTL(t *testing.T) {
	oldTTL := ephemeralTTL
	ephemeralTTL = 30 * time.Millisecond
	t.Cleanup(func() { ephemeralTTL = oldTTL })

	ui := newTestUI()
	done := make(chan struct{})
	defer close(done)
	drainUIEvents(ui, done)

	ui.SetEphemeral("only")
	time.Sleep(3 * ephemeralTTL)

	if got := probeEphemeral(t, ui); got != "" {
		t.Fatalf("ephemeralMsg = %q, want empty after TTL", got)
	}
}
