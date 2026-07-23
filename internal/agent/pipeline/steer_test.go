package pipeline

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestSteerMiddleware_DrainAll(t *testing.T) {
	ch := make(chan string, 5)
	ch <- "steer1"
	ch <- "steer2"
	ch <- "steer3"

	m := &SteerMiddleware{ch: ch}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	// All 3 queued steer messages should be drained in one call.
	expectedCount := 4 // initial + 3 steer
	if len(got.Messages) != expectedCount {
		t.Errorf("got %d messages, want %d (initial + 3 steer)", len(got.Messages), expectedCount)
	}

	// Verify [STEER] prefix.
	for i, msg := range got.Messages[1:] {
		expected := "[STEER] steer" + string(rune('1'+i))
		if msg.Content != expected {
			t.Errorf("message %d = %q, want %q", i+1, msg.Content, expected)
		}
	}
}

func TestSteerMiddleware_Empty(t *testing.T) {
	ch := make(chan string, 3)
	m := &SteerMiddleware{ch: ch}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	if len(got.Messages) != 1 {
		t.Errorf("got %d messages, want 1 (no queued)", len(got.Messages))
	}
}

func TestSteerMiddleware_ClosedChannel(t *testing.T) {
	ch := make(chan string, 2)
	ch <- "final-steer"
	close(ch)

	m := &SteerMiddleware{ch: ch}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	if len(got.Messages) != 2 {
		t.Errorf("got %d messages, want 2 (initial + final-steer from closed channel)", len(got.Messages))
	}
}

func TestSteerMiddleware_NilChannel(t *testing.T) {
	m := &SteerMiddleware{ch: nil}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	if got != step {
		t.Error("nil channel should return step unchanged")
	}
}

func TestSteerMiddleware_SkipsEmpty(t *testing.T) {
	ch := make(chan string, 3)
	ch <- ""
	ch <- "real-steer"
	ch <- ""

	m := &SteerMiddleware{ch: ch}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	if len(got.Messages) != 2 {
		t.Errorf("got %d messages, want 2 (initial + 1 real steer)", len(got.Messages))
	}
}

func TestSteerMiddleware_CompactsOnce(t *testing.T) {
	ch := make(chan string, 3)
	ch <- "steer-a"
	ch <- "steer-b"

	compactCalled := 0
	compactor := &testCompactor{
		fn: func(ctx context.Context, msgs []types.Message, threshold float64) []types.Message {
			compactCalled++
			// Return the same messages (no-op for test).
			return msgs
		},
	}

	m := &SteerMiddleware{ch: ch, compactor: compactor}
	step := &Step{Messages: []types.Message{types.UserMsg("initial")}}

	_, err := m.PrepareStep(context.Background(), step)
	if err != nil {
		t.Fatalf("PrepareStep: %v", err)
	}

	// Compaction should only be called once after draining all messages.
	if compactCalled != 1 {
		t.Errorf("compact called %d times, want 1 (once after draining all)", compactCalled)
	}
}

// testCompactor is a simple Compactor implementation for testing.
type testCompactor struct {
	fn func(context.Context, []types.Message, float64) []types.Message
}

func (c *testCompactor) Compact(ctx context.Context, msgs []types.Message, threshold float64) []types.Message {
	return c.fn(ctx, msgs, threshold)
}
