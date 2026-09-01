package tui

import (
	"strings"
	"testing"
	"time"
)

// TestRefreshMessages_AppendsNewTailOnly verifies the incremental fast
// path: appending items renders only the new chunk onto the existing
// buffer without forcing a full rebuild.
func TestRefreshMessages_AppendsNewTailOnly(t *testing.T) {
	ui := New("test")

	ui.appendMessage("hello there")
	ui.refreshMessages()
	if ui.renderedItems != 1 {
		t.Fatalf("expected renderedItems=1 after first render, got %d", ui.renderedItems)
	}
	before := ui.Messages.GetText(true)
	if !strings.Contains(before, "hello there") {
		t.Fatalf("first render missing user message; got %q", before)
	}

	ui.addAssistantResponse("streamed **response** tail")
	if ui.needsFullRender.Load() {
		t.Fatalf("appending a new item must not force a full rebuild")
	}

	ui.refreshMessages()
	after := ui.Messages.GetText(true)
	if !strings.HasPrefix(after, before) {
		t.Fatalf("append path rewrote existing content:\nbefore=%q\nafter=%q", before, after)
	}
	if !strings.Contains(after, "streamed") || !strings.Contains(after, "response") {
		t.Fatalf("append path dropped new content; got %q", after)
	}
	if ui.renderedItems != 2 {
		t.Fatalf("expected renderedItems=2, got %d", ui.renderedItems)
	}
}

// TestAddToolEnd_ForcesFullRender verifies that mutating an existing
// block in place invalidates the append fast path.
func TestAddToolEnd_ForcesFullRender(t *testing.T) {
	ui := New("test")

	ui.appendMessage("run something")
	ui.refreshMessages()

	ui.AddToolStart("42", "bash", "ls")
	ui.refreshMessages()
	if ui.needsFullRender.Load() {
		t.Fatalf("adding a NEW tool block must not force a full rebuild")
	}

	ui.AddToolEnd("42", "bash", "file output")
	if !ui.needsFullRender.Load() {
		t.Fatalf("completing an EXISTING tool block must force a full rebuild")
	}
}

// TestEnqueueUIEventDirect_SaturatesAtCap verifies that direct fallbacks
// beyond uiMaxDirectFallbacks are dropped and counted instead of piling up
// unbounded blocked goroutines.
func TestEnqueueUIEventDirect_SaturatesAtCap(t *testing.T) {
	ui := New("test")
	ui.bgDone = make(chan struct{})
	for i := 0; i < uiMaxDirectFallbacks; i++ {
		ui.fallbackSem <- struct{}{}
	}

	start := time.Now()
	ui.enqueueUIEventDirect(false, func() {})

	if ui.uiEventFallbackSat.Load() != 1 {
		t.Fatalf("expected fallback saturation counter = 1, got %d", ui.uiEventFallbackSat.Load())
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("saturation path should bail after the critical wait limit, took %v", elapsed)
	}
}

// TestEnqueueUIEventDirect_AbandonsAtShutdown verifies that in-flight
// fallback work is abandoned once the background loops are stopped, so no
// goroutine blocks forever on QueueUpdate of a stopped application.
func TestEnqueueUIEventDirect_AbandonsAtShutdown(t *testing.T) {
	ui := New("test")
	done := make(chan struct{})
	ui.bgDone = done
	close(done)

	ran := false
	ui.enqueueUIEventDirect(false, func() { ran = true })

	time.Sleep(50 * time.Millisecond)
	if ran {
		t.Fatalf("fallback work executed after shutdown")
	}
}

// TestSetCurrentPrompt_EchoWrapsAndTruncates pins the prompt echo
// behavior: short prompts fit on one row, long prompts truncate with
// an ellipsis instead of growing past promptEchoMaxLines.
func TestSetCurrentPrompt_EchoWrapsAndTruncates(t *testing.T) {
	ui := New("test")

	ui.SetCurrentPrompt("short prompt")
	short := ui.promptEcho.GetText(true)
	if !strings.Contains(short, "short prompt") {
		t.Fatalf("echo missing text: %q", short)
	}
	if strings.Contains(short, "…") {
		t.Errorf("short prompt should not truncate: %q", short)
	}

	ui.SetCurrentPrompt(strings.Repeat("x", 500))
	long := ui.promptEcho.GetText(true)
	if !strings.HasSuffix(long, "…") {
		t.Errorf("long prompt should end with ellipsis: %q", long)
	}
	if got := len([]rune(strings.TrimSuffix(long, "…"))); got > 500 {
		t.Errorf("truncated echo unexpectedly long: %d runes", got)
	}
}
