package pipeline

import (
	"context"
	"testing"
)

func TestConsolidateMiddleware_Name(t *testing.T) {
	m := NewConsolidateMiddleware(1, 0, func(context.Context, string) {})
	if got := m.Name(); got != "consolidate" {
		t.Fatalf("Name() = %q, want %q", got, "consolidate")
	}
}

func TestConsolidateMiddleware_PrepareStepAndPostModelNoOp(t *testing.T) {
	step := &Step{Iteration: 7}
	m := NewConsolidateMiddleware(1, 1, func(context.Context, string) { t.Fatal("unexpected dispatch") })

	got, err := m.PrepareStep(context.Background(), step)
	if err != nil || got != step {
		t.Fatalf("PrepareStep = (%v, %v), want same step, nil error", got, err)
	}
	got, err = m.PostModel(context.Background(), nil, step)
	if err != nil || got != step {
		t.Fatalf("PostModel = (%v, %v), want same step, nil error", got, err)
	}
}

func TestConsolidateMiddleware_NilDispatchIsInert(t *testing.T) {
	m := NewConsolidateMiddleware(1, 1, nil) // nil dispatch
	step := &Step{}
	for i := 0; i < 10; i++ {
		got, err := m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, step)
		if err != nil || got != step {
			t.Fatalf("PostTool #%d = (%v, %v), want same step, nil error", i, got, err)
		}
	}
}

func TestConsolidateMiddleware_ZeroIntervalsNeverFire(t *testing.T) {
	fires := 0
	m := NewConsolidateMiddleware(0, 0, func(context.Context, string) { fires++ })
	for i := 0; i < 10; i++ {
		_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, &Step{})
	}
	if fires != 0 {
		t.Fatalf("fired %d times with zero intervals, want 0", fires)
	}
}

func TestConsolidateMiddleware_TurnsInterval(t *testing.T) {
	var kinds []string
	m := NewConsolidateMiddleware(3, 0, func(_ context.Context, kind string) { kinds = append(kinds, kind) })

	// Three turns, one tool result each: fires exactly once on the third.
	for i := 0; i < 3; i++ {
		_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, &Step{})
	}
	if len(kinds) != 1 || kinds[0] != "memory" {
		t.Fatalf("fired %v, want exactly one %q dispatch", kinds, "memory")
	}
}

func TestConsolidateMiddleware_ItersCountsResults(t *testing.T) {
	fires := 0
	m := NewConsolidateMiddleware(0, 5, func(context.Context, string) { fires++ })

	// Two turns summing to 5 results fires once; a sixth result fires again.
	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "a"}, {Name: "b"}, {Name: "c"}}, &Step{})
	if fires != 0 {
		t.Fatalf("fired early after 3 results, want 0")
	}
	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "d"}, {Name: "e"}}, &Step{})
	if fires != 1 {
		t.Fatalf("fired %d times after 5 results, want 1", fires)
	}
}

func TestConsolidateMiddleware_ResetsAfterFire(t *testing.T) {
	fires := 0
	m := NewConsolidateMiddleware(2, 0, func(context.Context, string) { fires++ })

	for i := 0; i < 6; i++ {
		_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, &Step{})
	}
	if fires != 3 {
		t.Fatalf("fired %d times over 6 turns at interval 2, want 3", fires)
	}
}

func TestConsolidateMiddleware_TurnsTakePrecedence(t *testing.T) {
	fires := 0
	// Both intervals armed; turns (3) should cross before iters (10).
	m := NewConsolidateMiddleware(3, 10, func(context.Context, string) { fires++ })

	for i := 0; i < 3; i++ {
		_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, &Step{})
	}
	if fires != 1 {
		t.Fatalf("fired %d times, want exactly 1 (turns take precedence)", fires)
	}
}

func TestConsolidateMiddleware_ItersResetsTurnCount(t *testing.T) {
	fires := 0
	// iters interval crossed resets BOTH counters, so the subsequent turns
	// must not fire a turns-based dispatch early.
	m := NewConsolidateMiddleware(100, 3, func(context.Context, string) { fires++ })

	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "a"}, {Name: "b"}, {Name: "c"}}, &Step{})
	if fires != 1 {
		t.Fatalf("fired %d times after iters interval, want 1", fires)
	}
	// Two more turns: turnsSince is now 2, still under the (huge) turns interval.
	for i := 0; i < 2; i++ {
		_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, &Step{})
	}
	if fires != 1 {
		t.Fatalf("fired %d times after iters reset, want still 1", fires)
	}
}
