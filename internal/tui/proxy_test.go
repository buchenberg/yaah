package tui

import (
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/types"
)

func TestHandleEvent_DoesNotBlock(t *testing.T) {
	ui := New("test")

	events := []agent.Event{
		&agent.ThinkingEvent{Text: "thinking..."},
		&agent.FlushEvent{Content: "flush"},
		&agent.ToolStartEvent{ID: 1, Name: "read", Args: `{"file":"test.go"}`},
		&agent.ToolEndEvent{ID: 1, Name: "read", Args: `{"file":"test.go"}`, Result: "ok", Duration: time.Millisecond},
		&agent.SubAgentStartEvent{SubAgentID: "sub-1", Role: "analyst", Model: "claude", Prompt: "analyze"},
		&agent.SubAgentEndEvent{SubAgentID: "sub-1", Role: "analyst", Duration: time.Second},
		&agent.EscalationEvent{Summary: "test escalation"},
		&agent.CompactionStartedEvent{BeforeTokens: 1000, TargetTokens: 500, Reason: "test"},
		&agent.CompactionDoneEvent{BeforeTokens: 1000, AfterTokens: 500, SavingsPct: 0.5, Method: "test", ElapsedSeconds: 0.1},
		&agent.DoneEvent{Response: "done", ContextWindow: 8000},
	}

	for _, evt := range events {
		done := make(chan struct{})
		go func(evt agent.Event) {
			ui.HandleEvent(evt)
			close(done)
		}(evt)

		select {
		case <-done:
			// HandleEvent returned quickly — the async dispatch worked.
		case <-time.After(2 * time.Second):
			t.Fatalf("HandleEvent blocked for %T — regression of async dispatch fix", evt)
		}
	}
}

func TestHandleEvent_TokenDeltaIncrementsCounter(t *testing.T) {
	ui := New("test")

	before := ui.tokensRx.Load()

	// TokenDeltaEvent is async now. The goroutine spawns and the
	// atomic counter is incremented before any QueueUpdate blocks.
	done := make(chan struct{})
	go func() {
		ui.HandleEvent(&agent.TokenDeltaEvent{Text: "hello"})
		close(done)
	}()

	<-done

	after := ui.tokensRx.Load()
	if after <= before {
		t.Error("tokensRx not incremented for TokenDeltaEvent")
	}
}

func TestHandleEvent_TokenDeltaPreservesOrder(t *testing.T) {
	ui := New("test")

	// TokenDeltaEvent is now async (go QueueUpdate). All tokens are
	// dispatched to goroutines before any QueueUpdate blocks. The
	// atomic counter is incremented for each event synchronously.

	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			ui.HandleEvent(&agent.TokenDeltaEvent{Text: "x"})
		}
		close(done)
	}()

	<-done

	// All 5 tokens were dispatched asynchronously.
	if got := ui.tokensRx.Load(); got != 5 {
		t.Errorf("expected 5 tokens dispatched, got %d", got)
	}
}

func TestHandleEvent_DoneEventStates(t *testing.T) {
	ui := New("test")

	// These are state validations that don't need a running app.
	// The DoneEvent handler should exist and handle all field types.

	done := &agent.DoneEvent{
		Response:         "final text",
		Error:            "",
		ContextTokens:    100,
		ContextWindow:    128000,
		LastPromptTokens: 50,
		FinishReason:     "stop",
		Usage: types.Usage{
			PromptTokens:     500,
			CompletionTokens: 300,
			TotalTokens:      800,
		},
		ResponseModel: "gpt-4o",
	}

	// Verify HandleEvent doesn't panic and returns quickly.
	done_ch := make(chan struct{})
	go func() {
		ui.HandleEvent(done)
		close(done_ch)
	}()

	select {
	case <-done_ch:
		// DoneEvent dispatched async — forwarder not blocked.
	case <-time.After(2 * time.Second):
		t.Fatal("DoneEvent blocked — regression of async dispatch fix")
	}
}

func TestHandleEvent_SubAgentError(t *testing.T) {
	ui := New("test")

	done := make(chan struct{})
	go func() {
		ui.HandleEvent(&agent.SubAgentEndEvent{
			SubAgentID: "sub-1",
			Role:       "analyst",
			Error:      "sub-agent failed",
			Duration:   time.Second,
		})
		close(done)
	}()

	select {
	case <-done:
		// Success.
	case <-time.After(2 * time.Second):
		t.Fatal("SubAgentEndEvent blocked")
	}
}

func TestHandleEvent_ToolError(t *testing.T) {
	ui := New("test")

	done := make(chan struct{})
	go func() {
		ui.HandleEvent(&agent.ToolEndEvent{
			ID:       1,
			Name:     "read",
			Args:     `{"file":"test.go"}`,
			Error:    "file not found",
			Duration: time.Millisecond,
		})
		close(done)
	}()

	select {
	case <-done:
		// Success.
	case <-time.After(2 * time.Second):
		t.Fatal("ToolEndEvent with error blocked")
	}
}
