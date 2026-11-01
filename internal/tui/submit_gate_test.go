package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

// Enter must queue a follow-up whenever a turn is in flight, even when no
// content tokens are streaming. isStreaming is false while the model thinks,
// while tools run, and between iterations; using it as the "busy" signal
// started a second concurrent prompt on the same session, whose stale
// message snapshot lost the running turn and wedged persistence.
func TestGlobalInputCapture_SendDuringActiveRunQueuesFollowUp(t *testing.T) {
	ui := newColonUI(t)
	var submitted, followed string
	ui.OnSubmit = func(s string) { submitted = s }
	ui.OnFollowUp = func(s string) { followed = s }

	ui.agentActive = true
	ui.activityBusy.Store(true)
	ui.isStreaming.Store(false) // tool work / thinking: no tokens in flight
	ui.Input.SetText("and then?", true)

	ev := tcell.NewEventKey(tcell.KeyEnter, '\r', tcell.ModNone)
	if ret := ui.globalInputCapture(ev); ret != nil {
		t.Fatal("Enter not consumed while a turn was active")
	}
	if submitted != "" {
		t.Fatalf("started a second prompt during an active turn: %q", submitted)
	}
	if followed != "and then?" {
		t.Fatalf("followed = %q, want %q", followed, "and then?")
	}
	if got := ui.Input.GetText(); got != "" {
		t.Fatalf("composer = %q, want it cleared after queueing", got)
	}
}

// The same key must still submit a fresh prompt when the session is idle.
func TestGlobalInputCapture_SendWhileIdleStartsPrompt(t *testing.T) {
	ui := newColonUI(t)
	var submitted, followed string
	ui.OnSubmit = func(s string) { submitted = s }
	ui.OnFollowUp = func(s string) { followed = s }

	ui.agentActive = false
	ui.activityBusy.Store(false)
	ui.isStreaming.Store(false)
	ui.Input.SetText("hello", true)

	ev := tcell.NewEventKey(tcell.KeyEnter, '\r', tcell.ModNone)
	if ret := ui.globalInputCapture(ev); ret != nil {
		t.Fatal("Enter not consumed while idle")
	}
	if followed != "" {
		t.Fatalf("queued a follow-up for an idle session: %q", followed)
	}
	if submitted != "hello" {
		t.Fatalf("submitted = %q, want %q", submitted, "hello")
	}
}
