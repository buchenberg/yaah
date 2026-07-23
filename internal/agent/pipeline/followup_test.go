package pipeline

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestFollowupMiddleware_DrainAll(t *testing.T) {
	ch := make(chan string, 5)
	ch <- "msg1"
	ch <- "msg2"
	ch <- "msg3"

	m := &FollowupMiddleware{ch: ch}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	// All 3 queued messages should be drained in one call.
	expectedCount := 4 // initial + msg1 + msg2 + msg3
	if len(got.Messages) != expectedCount {
		t.Errorf("got %d messages, want %d (initial + 3 queued)", len(got.Messages), expectedCount)
	}
}

func TestFollowupMiddleware_Empty(t *testing.T) {
	ch := make(chan string, 3)
	m := &FollowupMiddleware{ch: ch}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	if len(got.Messages) != 1 {
		t.Errorf("got %d messages, want 1 (no queued)", len(got.Messages))
	}
}

func TestFollowupMiddleware_ClosedChannel(t *testing.T) {
	ch := make(chan string, 2)
	ch <- "last-msg"
	close(ch)

	m := &FollowupMiddleware{ch: ch}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	if len(got.Messages) != 2 {
		t.Errorf("got %d messages, want 2 (initial + last-msg from closed channel)", len(got.Messages))
	}
}

func TestFollowupMiddleware_NilChannel(t *testing.T) {
	m := &FollowupMiddleware{ch: nil}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	if got != step {
		t.Error("nil channel should return step unchanged")
	}
}

func TestFollowupMiddleware_SkipsEmpty(t *testing.T) {
	ch := make(chan string, 3)
	ch <- ""
	ch <- "real-msg"
	ch <- ""

	m := &FollowupMiddleware{ch: ch}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	// Only "real-msg" should be appended (empty strings skipped).
	if len(got.Messages) != 2 {
		t.Errorf("got %d messages, want 2 (initial + 1 real)", len(got.Messages))
	}
}
