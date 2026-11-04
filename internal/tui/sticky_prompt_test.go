package tui

import (
	"strings"
	"testing"

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

// TestPromptAnchors_AppendMatchesFull pins anchor-table integrity: the
// table delta-maintained during incremental appends must equal the one a
// full rebuild computes for the same log.
func TestPromptAnchors_AppendMatchesFull(t *testing.T) {
	ui := New("test")
	ui.AddPrompt("a reasonably long question that might wrap on narrower panes")
	ui.refreshMessages()

	ui.AddToolStart("1", "bash", "ls")
	ui.refreshMessages()
	// The tool chunk took the append path; the table is its delta result.
	appended := append([]promptAnchor(nil), ui.promptAnchors...)

	ui.needsFullRender.Store(true)
	ui.refreshMessages()
	full := ui.promptAnchors

	if len(appended) != len(full) {
		t.Fatalf("anchor counts differ: %d vs %d", len(appended), len(full))
	}
	for i := range full {
		if appended[i] != full[i] {
			t.Fatalf("anchor %d drifted between append and full: %+v vs %+v",
				i, appended[i], full[i])
		}
	}
}
