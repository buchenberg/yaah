package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// TestAddPrompt_RendersInStream_DoesNotPin verifies the submit-path
// change: the question renders in the conversation stream, and the
// sticky header stays empty — the pin is scroll-derived, not submit-
// derived.
func TestAddPrompt_RendersInStream_DoesNotPin(t *testing.T) {
	ui := New("test")
	ui.promptAnchors = nil

	ui.AddPrompt("what about the swap path?")
	if ui.pinnedPrompt != "" {
		t.Fatalf("AddPrompt must not pin the prompt, got %q", ui.pinnedPrompt)
	}
	ui.refreshMessages()

	buf := ui.Messages.GetText(true)
	if !strings.Contains(buf, "🍖") || !strings.Contains(buf, "what about the swap path?") {
		t.Fatalf("prompt missing from conversation stream: %q", buf)
	}
	if ui.pinnedPrompt != "" || ui.promptEcho.GetText(true) != "" {
		t.Fatalf("prompt pinned before crossing the seam: pin=%q echo=%q",
			ui.pinnedPrompt, ui.promptEcho.GetText(true))
	}
	if len(ui.promptAnchors) != 1 || ui.promptAnchors[0].input != "what about the swap path?" {
		t.Fatalf("anchor table wrong: %+v", ui.promptAnchors)
	}
}

// TestReconcilePromptPin_Rules drives the scroll position through the
// full lifecycle: no pin below the seam, pin on crossing, swap to the
// newer prompt when it crosses, reverse scroll re-pins the previous one.
func TestReconcilePromptPin_Rules(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(120, 24)
	ui := New("test")
	ui.Root.SetRect(0, 0, 120, 24)
	ui.Root.Draw(sim)

	ui.AddPrompt("question one")
	ui.refreshMessages()
	if len(ui.promptAnchors) != 1 {
		t.Fatalf("expected one anchor, got %+v", ui.promptAnchors)
	}
	q1Line := ui.promptAnchors[0].startLine

	// Below the seam: unpinned.
	ui.Messages.ScrollTo(0, 0)
	ui.reconcilePromptPin()
	if ui.pinnedPrompt != "" {
		t.Fatalf("prompt pinned while below the seam: %q", ui.pinnedPrompt)
	}

	// Crossing the seam: pinned. The echo shows the question text and
	// the stream still holds its own copy.
	ui.Messages.ScrollTo(q1Line, 0)
	ui.reconcilePromptPin()
	if ui.pinnedPrompt != "question one" {
		t.Fatalf("seam crossing did not pin; got %q", ui.pinnedPrompt)
	}
	if !strings.Contains(ui.promptEcho.GetText(true), "question one") {
		t.Fatalf("echo missing pinned prompt: %q", ui.promptEcho.GetText(true))
	}
	if !strings.Contains(ui.Messages.GetText(true), "question one") {
		t.Fatalf("in-stream copy lost: %q", ui.Messages.GetText(true))
	}

	// A second question, still far below the seam: the pin holds q1.
	ui.AddPrompt("question two")
	ui.AddToolStart("1", "bash", "ls") // grows the buffer via the fast path
	ui.refreshMessages()
	if len(ui.promptAnchors) != 2 {
		t.Fatalf("expected two anchors, got %+v", ui.promptAnchors)
	}
	q2 := ui.promptAnchors[1]
	if q2.startLine <= q1Line {
		t.Fatalf("q2 anchor not below q1: %+v", ui.promptAnchors)
	}
	ui.reconcilePromptPin() // scroll is still near q1
	if ui.pinnedPrompt != "question one" {
		t.Fatalf("second question took the pin before crossing the seam: %q", ui.pinnedPrompt)
	}

	// q2 crosses the seam: swap.
	ui.Messages.ScrollTo(q2.startLine, 0)
	ui.reconcilePromptPin()
	if ui.pinnedPrompt != "question two" {
		t.Fatalf("q2 did not take over the pin: %q", ui.pinnedPrompt)
	}

	// Reverse scroll above q2: q2 detaches, q1 re-pins.
	ui.Messages.ScrollTo(q1Line+1, 0)
	ui.reconcilePromptPin()
	if ui.pinnedPrompt != "question one" {
		t.Fatalf("reverse scroll did not re-pin q1: %q", ui.pinnedPrompt)
	}

	// And above q1: everything detaches; the echo hides.
	ui.Messages.ScrollTo(0, 0)
	ui.reconcilePromptPin()
	if ui.pinnedPrompt != "" || ui.promptEcho.GetText(true) != "" {
		t.Fatalf("echo did not hide above every prompt: pin=%q echo=%q",
			ui.pinnedPrompt, ui.promptEcho.GetText(true))
	}
}

// TestPromptAnchors_AppendMatchesFull pins anchor-table integrity: after
// a stretch of incremental renders (fast-path tool appends, in-place
// block mutations with their cache invalidations, full-rendered text
// flushes), the delta-maintained table must equal what a full recompute
// produces for the same log.
func TestPromptAnchors_AppendMatchesFull(t *testing.T) {
	ui := New("test")
	ui.AddPrompt("a reasonably long question that might wrap on narrower panes")
	ui.refreshMessages()

	ui.AddToolStart("1", "bash", "ls")
	ui.refreshMessages()

	// In-place expand toggle of the completed tool block: the row count
	// changes with the block, so the cache must remeasure.
	ui.AddToolEnd("1", "bash · ls", strings.Repeat("line\n", 8))
	ui.refreshMessages()

	ui.AddPrompt("second question after the tool output")
	fullRendersBefore := ui.fullRenderCount
	ui.refreshMessages()
	if ui.fullRenderCount == fullRendersBefore {
		t.Fatalf("text flush must take the full-render path")
	}
	ui.AddToolStart("2", "grep", "pattern")
	ui.refreshMessages()

	// The completed tool block is the first toggleable block.
	ui.ToggleBlockByIndex(0)
	ui.refreshMessages()

	// All incremental paths exercised; capture the delta-maintained table.
	appended := append([]promptAnchor(nil), ui.promptAnchors...)

	ui.needsFullRender.Store(true)
	ui.refreshMessages()
	full := ui.promptAnchors

	if len(appended) != len(full) {
		t.Fatalf("anchor counts differ: %d vs %d", len(appended), len(full))
	}
	for i := range full {
		if appended[i] != full[i] {
			t.Fatalf("anchor %d drifted between incremental and full: %+v vs %+v",
				i, appended[i], full[i])
		}
	}
}

// TestReconcilePromptPin_DeadlockSafety pins the freeze fix: tview
// invokes the AfterDraw hook while still holding the application lock,
// so the pin-change path must not take that lock again (SetFocus used
// to, freezing the UI at the first pin swap). A pin change is queued
// via the scroll position before the draw; the draw must return and
// leave focus untouched.
func TestReconcilePromptPin_DeadlockSafety(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer sim.Fini()
	sim.SetSize(120, 30)
	ui := New("test")
	ui.App.SetScreen(sim)

	ui.AddPrompt("question one")
	ui.refreshMessages()
	if len(ui.promptAnchors) != 1 {
		t.Fatalf("expected one anchor, got %+v", ui.promptAnchors)
	}

	// Content taller than the pane: the refresh's auto-scroll carries the
	// viewport past the prompt's seam, so the draw below has a pin change
	// pending (empty -> question one).
	ui.tokenMu.Lock()
	ui.pendingTokens.WriteString(strings.Repeat("word ", 800))
	ui.isStreaming.Store(true)
	ui.tokenMu.Unlock()
	if !ui.flushPendingTokens() {
		t.Fatalf("flush did not render the streamed response")
	}
	ui.refreshMessages()

	focusBefore := ui.App.GetFocus()

	time.AfterFunc(10*time.Second, func() {
		panic("reconcilePromptPin deadlocked inside the application draw (app lock re-entered)")
	})

	ui.App.ForceDraw()

	if ui.pinnedPrompt != "question one" {
		t.Fatalf("pin change did not land during the draw: %q", ui.pinnedPrompt)
	}
	if ui.App.GetFocus() != focusBefore {
		t.Fatalf("pin reconciliation stole focus")
	}

	// Painted-row validation: a second question past the wrapped response
	// gives an anchor whose startLine depends on the wrap estimate; it
	// must match the row tview actually painted (offset math: painted =
	// innerTop + startLine - scrollOffset).
	ui.AddPrompt("question two")
	ui.refreshMessages()
	ui.App.ForceDraw()

	ro, _ := ui.Messages.GetScrollOffset()
	_, innerTop, _, innerH := ui.Messages.GetInnerRect()
	expected := innerTop + ui.promptAnchors[1].startLine - ro
	if expected < innerTop || expected >= innerTop+innerH {
		t.Fatalf("anchored row outside viewport: expected=%d innerTop=%d innerH=%d offset=%d",
			expected, innerTop, innerH, ro)
	}

	cells, w, _ := sim.GetContents()
	var b strings.Builder
	for x := 0; x < w; x++ {
		i := expected*w + x
		if i >= len(cells) || len(cells[i].Runes) == 0 {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(cells[i].Runes[0])
	}
	if !strings.Contains(strings.TrimRight(b.String(), " "), "question two") {
		t.Fatalf("anchor startLine %d does not match the painted row %d (%q)",
			ui.promptAnchors[1].startLine, expected, b.String())
	}
}
