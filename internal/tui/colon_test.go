package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// newColonUI builds a test App whose UI event queue is drained, so colon
// dispatch can run without a screen.
func newColonUI(t *testing.T) *App {
	t.Helper()
	oldTTL := ephemeralTTL
	ephemeralTTL = time.Hour // keep the auto-clear timer out of the way
	t.Cleanup(func() { ephemeralTTL = oldTTL })

	ui := newTestUI()
	done := make(chan struct{})
	drainUIEvents(ui, done)
	t.Cleanup(func() { close(done) })
	return ui
}

func TestSubmitInput_RoutesColonSteerToTheStepper(t *testing.T) {
	ui := newColonUI(t)
	ui.agentActive = true // steering only applies to a turn in flight
	var steered, submitted string
	ui.OnSteer = func(s string) { steered = s }
	ui.OnSubmit = func(s string) { submitted = s }

	ui.Input.SetText(":steer keep it short", true)
	ui.submitInput()

	if steered != "keep it short" {
		t.Fatalf("steered = %q, want %q", steered, "keep it short")
	}
	if submitted != "" {
		t.Fatalf("colon command reached the model: %q", submitted)
	}
	if got := ui.Input.GetText(); got != "" {
		t.Fatalf("input not cleared after command: %q", got)
	}
}

func TestSubmitFollowUp_RoutesColonFollowUp(t *testing.T) {
	ui := newColonUI(t)
	ui.agentActive = true // a follow-up is queued behind a running turn
	var followed, submitted string
	ui.OnFollowUp = func(s string) { followed = s }
	ui.OnSubmit = func(s string) { submitted = s }

	ui.Input.SetText(":followup and then?", true)
	ui.submitFollowUp()

	if followed != "and then?" {
		t.Fatalf("followed = %q, want %q", followed, "and then?")
	}
	if submitted != "" {
		t.Fatalf("colon command reached the model: %q", submitted)
	}
}

func TestSubmitInput_BareArgCommandReportsUsage(t *testing.T) {
	ui := newColonUI(t)
	called := false
	ui.OnSteer = func(string) { called = true }

	ui.Input.SetText(":steer", true)
	ui.submitInput()

	if called {
		t.Fatal("steer ran without an argument")
	}
	if msg := probeEphemeral(t, ui); !strings.Contains(msg, "usage:") {
		t.Fatalf("ephemeral = %q, want a usage message", msg)
	}
	if got := ui.Input.GetText(); got != ":steer" {
		t.Fatalf("input = %q, want the text retained for editing", got)
	}
}

func TestSubmitInput_UnknownCommandIsLoud(t *testing.T) {
	ui := newColonUI(t)
	var submitted string
	ui.OnSubmit = func(s string) { submitted = s }

	ui.Input.SetText(":seach typo", true)
	ui.submitInput()

	if submitted != "" {
		t.Fatalf("unknown command sent to the model: %q", submitted)
	}
	msg := probeEphemeral(t, ui)
	if !strings.Contains(msg, "unknown command") || !strings.Contains(msg, "seach") {
		t.Fatalf("ephemeral = %q, want an unknown-command message naming the word", msg)
	}
}

// A message that merely starts with ':' must not be swallowed: ":)" is chat,
// not a command.
func TestSubmitInput_NonCommandColonTextIsSentAsPrompt(t *testing.T) {
	for _, text := range []string{":)", ":3", ":", ":  "} {
		t.Run(text, func(t *testing.T) {
			ui := newColonUI(t)
			var submitted string
			ui.OnSubmit = func(s string) { submitted = s }

			ui.Input.SetText(text, true)
			ui.submitInput()

			if submitted != text {
				t.Fatalf("submitted = %q, want %q", submitted, text)
			}
		})
	}
}

// Esc must stop the running turn from the message view too, not only while the
// input box holds focus.
func TestGlobalInputCapture_EscapeStopsFromAnyNormalFocus(t *testing.T) {
	ui := newColonUI(t)
	stopped := false
	ui.OnStop = func() { stopped = true }
	ui.OnAbort = func() { t.Fatal("Esc fell back to OnAbort") }
	ui.focus = focusNormal

	ev := tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)
	if ret := ui.globalInputCapture(ev); ret != nil {
		t.Fatal("Esc not consumed at normal focus")
	}
	if !stopped {
		t.Fatal("Esc did not stop the turn")
	}
}

// ...but it must not steal Escape from a modal or the palette, which use it to
// dismiss themselves.
func TestGlobalInputCapture_EscapeLeavesLayeredViewsAlone(t *testing.T) {
	for _, focus := range []focusState{focusCommandPalette, focusModal} {
		ui := newColonUI(t)
		stopped := false
		ui.OnStop = func() { stopped = true }
		ui.focus = focus

		ev := tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)
		if ret := ui.globalInputCapture(ev); ret == nil {
			t.Fatalf("focus %v: Esc consumed by the app instead of the layer", focus)
		}
		if stopped {
			t.Fatalf("focus %v: Esc stopped the turn while a layer was open", focus)
		}
	}
}

// Steering a turn that is not running must be refused: the text would otherwise
// sit in the steer channel and be injected into the first step of the NEXT turn.
func TestHandleCommand_SteerRefusedWhenNoTurnIsRunning(t *testing.T) {
	ui := newColonUI(t)
	var steered string
	ui.OnSteer = func(s string) { steered = s }
	ui.agentActive = false

	ui.Input.SetText(":steer keep it short", true)
	ui.submitInput()

	if steered != "" {
		t.Fatalf("steered an idle session: %q", steered)
	}
	if msg := probeEphemeral(t, ui); !strings.Contains(msg, "no turn") {
		t.Fatalf("ephemeral = %q, want a no-turn-running message", msg)
	}
}

func TestHandleCommand_SteerWorksDuringTurn(t *testing.T) {
	ui := newColonUI(t)
	var steered string
	ui.OnSteer = func(s string) { steered = s }
	ui.agentActive = true

	ui.Input.SetText(":steer keep it short", true)
	ui.submitInput()

	if steered != "keep it short" {
		t.Fatalf("steered = %q, want %q", steered, "keep it short")
	}
}

// A follow-up with no turn to follow belongs in the conversation as a prompt —
// not silently queued for a later turn's follow-up drain.
func TestHandleCommand_FollowUpSendsAsPromptWhenIdle(t *testing.T) {
	ui := newColonUI(t)
	var submitted, followed string
	ui.OnSubmit = func(s string) { submitted = s }
	ui.OnFollowUp = func(s string) { followed = s }
	ui.agentActive = false

	ui.Input.SetText(":followup and then?", true)
	ui.submitInput()

	if followed != "" {
		t.Fatalf("queued a follow-up for an idle session: %q", followed)
	}
	if submitted != "and then?" {
		t.Fatalf("submitted = %q, want it sent as a message", submitted)
	}
}

func TestHandleCommand_FollowUpQueuedDuringTurn(t *testing.T) {
	ui := newColonUI(t)
	var submitted, followed string
	ui.OnSubmit = func(s string) { submitted = s }
	ui.OnFollowUp = func(s string) { followed = s }
	ui.agentActive = true

	ui.Input.SetText(":followup and then?", true)
	ui.submitInput()

	if followed != "and then?" {
		t.Fatalf("followed = %q, want %q", followed, "and then?")
	}
	if submitted != "" {
		t.Fatalf("sent as a prompt instead of a follow-up: %q", submitted)
	}
}
