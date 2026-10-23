package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// typeText puts text in the composer and recomputes the typeahead strip, which
// is what SetText's changed handler does in the running UI.
func typeText(ui *App, text string) {
	ui.Input.SetText(text, true)
	ui.updateSuggestions()
}

func TestUpdateSuggestions_ShowsMatchesAndSizesStrip(t *testing.T) {
	ui := newColonUI(t)
	typeText(ui, ":st")

	if !ui.hasSuggestions() {
		t.Fatal("no suggestions for :st")
	}
	labels := make([]string, len(ui.suggestions))
	for i, s := range ui.suggestions {
		labels[i] = s.Label
	}
	for _, want := range []string{"stop", "steer"} {
		if !strings.Contains(strings.Join(labels, ","), want) {
			t.Fatalf("suggestions = %v, want %q among them", labels, want)
		}
	}
	if got := ui.suggestionsView.GetText(true); !strings.Contains(got, ":steer") {
		t.Fatalf("strip text = %q, want it to name the match", got)
	}
}

func TestUpdateSuggestions_HidesWhenNotApplicable(t *testing.T) {
	ui := newColonUI(t)
	typeText(ui, ":st")
	if !ui.hasSuggestions() {
		t.Fatal("precondition: suggestions should show for :st")
	}
	for _, text := range []string{":steer keep it short", "hello", ""} {
		typeText(ui, text)
		if ui.hasSuggestions() {
			t.Fatalf("suggestions still shown for %q", text)
		}
		if got := ui.suggestionsView.GetText(true); got != "" {
			t.Fatalf("strip text = %q, want empty for %q", got, text)
		}
	}
}

func TestAcceptSuggestion_CompletesCommandAndHides(t *testing.T) {
	ui := newColonUI(t)
	typeText(ui, ":ste")

	if !ui.acceptSuggestion() {
		t.Fatal("acceptSuggestion reported nothing to accept")
	}
	if got := ui.Input.GetText(); got != ":steer " {
		t.Fatalf("composer = %q, want %q", got, ":steer ")
	}
	if ui.hasSuggestions() {
		t.Fatal("strip still visible after accepting")
	}
}

func TestCycleSuggestion_WrapsBothWays(t *testing.T) {
	ui := newColonUI(t)
	typeText(ui, ":s")
	n := len(ui.suggestions)
	if n < 2 {
		t.Fatalf("need at least two matches for :s, got %d", n)
	}
	if ui.suggestionIdx != 0 {
		t.Fatalf("initial index = %d, want 0", ui.suggestionIdx)
	}
	ui.cycleSuggestion(1)
	if ui.suggestionIdx != 1 {
		t.Fatalf("after down: index = %d, want 1", ui.suggestionIdx)
	}
	ui.cycleSuggestion(-1)
	if ui.suggestionIdx != 0 {
		t.Fatalf("after up: index = %d, want 0", ui.suggestionIdx)
	}
	ui.cycleSuggestion(-1)
	if ui.suggestionIdx != n-1 {
		t.Fatalf("wrapped index = %d, want %d", ui.suggestionIdx, n-1)
	}
}

// Tab accepts the highlighted match while the strip is up, and Tab is passed
// through untouched when it is not.
func TestGlobalInputCapture_TabAcceptsSuggestion(t *testing.T) {
	ui := newColonUI(t)
	typeText(ui, ":ste")

	tab := tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
	if ret := ui.globalInputCapture(tab); ret != nil {
		t.Fatal("Tab not consumed while the strip was visible")
	}
	if got := ui.Input.GetText(); got != ":steer " {
		t.Fatalf("composer = %q, want %q", got, ":steer ")
	}

	if ret := ui.globalInputCapture(tab); ret == nil {
		t.Fatal("Tab consumed with no suggestions visible")
	}
}

func TestGlobalInputCapture_ArrowsCycleOnlyWhileVisible(t *testing.T) {
	ui := newColonUI(t)
	typeText(ui, ":s")

	down := tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)
	if ret := ui.globalInputCapture(down); ret != nil {
		t.Fatal("Down not consumed while the strip was visible")
	}
	if ui.suggestionIdx != 1 {
		t.Fatalf("index = %d, want 1", ui.suggestionIdx)
	}

	ui.hideSuggestions()
	if ret := ui.globalInputCapture(down); ret == nil {
		t.Fatal("Down consumed with no suggestions visible")
	}
}

// Escape must dismiss the strip without stopping the turn — the user is editing
// a command name, not asking to abort.
func TestGlobalInputCapture_EscapeDismissesSuggestionsBeforeStopping(t *testing.T) {
	ui := newColonUI(t)
	stopped := false
	ui.OnStop = func() { stopped = true }
	typeText(ui, ":st")

	esc := tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)
	if ret := ui.globalInputCapture(esc); ret != nil {
		t.Fatal("Esc not consumed while dismissing the strip")
	}
	if ui.hasSuggestions() {
		t.Fatal("strip still visible after Esc")
	}
	if stopped {
		t.Fatal("Esc stopped the turn instead of just dismissing the strip")
	}

	// A second Esc, with nothing to dismiss, does stop the turn.
	if ret := ui.globalInputCapture(esc); ret != nil {
		t.Fatal("second Esc not consumed")
	}
	if !stopped {
		t.Fatal("second Esc did not stop the turn")
	}
}
