package pipeline

import (
	"context"
	"testing"
	"time"
)

// dispatchRecorder captures dispatch calls on a buffered channel so tests can
// assert on fire-and-forget dispatches without data races.
type dispatchRecorder struct {
	ch chan string
}

func newRecorder() *dispatchRecorder {
	return &dispatchRecorder{ch: make(chan string, 32)}
}

func (r *dispatchRecorder) fn() ConsolidateDispatch {
	return func(_ context.Context, kind string) { r.ch <- kind }
}

// wait blocks until want dispatches arrive, then asserts no extra dispatch
// followed. It also verifies each dispatch carries kind "memory".
func (r *dispatchRecorder) wait(t *testing.T, want int) {
	t.Helper()
	for i := 0; i < want; i++ {
		select {
		case kind := <-r.ch:
			if kind != "memory" {
				t.Fatalf("dispatch %d kind = %q, want %q", i+1, kind, "memory")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for dispatch %d/%d", i+1, want)
		}
	}
	r.assertNone(t)
}

func (r *dispatchRecorder) assertNone(t *testing.T) {
	t.Helper()
	select {
	case kind := <-r.ch:
		t.Fatalf("unexpected dispatch %q", kind)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestConsolidateMiddleware_Name(t *testing.T) {
	m := NewConsolidateMiddleware(1, 0, func(context.Context, string) {})
	if got := m.Name(); got != "consolidate" {
		t.Fatalf("Name() = %q, want %q", got, "consolidate")
	}
}

// TestConsolidateBuilderRegistered locks in that the consolidate builder lives
// in builtinBuilders (the orchestrator map) — a prior revision registered it in
// subAgentBuilders, where middleware.enabled could never reach it.
func TestConsolidateBuilderRegistered(t *testing.T) {
	if _, ok := builtinBuilders["consolidate"]; !ok {
		t.Fatal("consolidate not registered in builtinBuilders (orchestrator map)")
	}
	if _, ok := subAgentBuilders["consolidate"]; ok {
		t.Fatal("consolidate must not be registered in subAgentBuilders")
	}
	builder := builtinBuilders["consolidate"]
	m := builder(PipelineConfig{
		ConsolidateTurnsInterval: 1,
		ConsolidateDispatch:      func(context.Context, string) {},
	})
	if m.Name() != "consolidate" {
		t.Fatalf("builder produced middleware named %q, want %q", m.Name(), "consolidate")
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
	r := newRecorder()
	m := NewConsolidateMiddleware(0, 0, r.fn())
	for i := 0; i < 10; i++ {
		_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, &Step{})
	}
	r.assertNone(t)
}

func TestConsolidateMiddleware_TurnsInterval(t *testing.T) {
	r := newRecorder()
	m := NewConsolidateMiddleware(3, 0, r.fn())

	// Three turns, one tool result each: fires exactly once on the third.
	for i := 0; i < 3; i++ {
		_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, &Step{})
	}
	r.wait(t, 1)
}

func TestConsolidateMiddleware_ItersCountsResults(t *testing.T) {
	r := newRecorder()
	m := NewConsolidateMiddleware(0, 5, r.fn())

	// Two turns summing to 5 results fires once; the first turn must not fire.
	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "a"}, {Name: "b"}, {Name: "c"}}, &Step{})
	r.assertNone(t)
	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "d"}, {Name: "e"}}, &Step{})
	r.wait(t, 1)
}

func TestConsolidateMiddleware_ResetsAfterFire(t *testing.T) {
	r := newRecorder()
	m := NewConsolidateMiddleware(2, 0, r.fn())

	for i := 0; i < 6; i++ {
		_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, &Step{})
	}
	r.wait(t, 3)
}

func TestConsolidateMiddleware_TurnsTakePrecedence(t *testing.T) {
	r := newRecorder()
	// Both intervals armed; turns (3) should cross before iters (10).
	m := NewConsolidateMiddleware(3, 10, r.fn())

	for i := 0; i < 3; i++ {
		_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, &Step{})
	}
	r.wait(t, 1)
}

func TestConsolidateMiddleware_ItersResetsTurnCount(t *testing.T) {
	r := newRecorder()
	// iters interval crossed resets BOTH counters, so the subsequent turns
	// must not fire a turns-based dispatch early.
	m := NewConsolidateMiddleware(100, 3, r.fn())

	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "a"}, {Name: "b"}, {Name: "c"}}, &Step{})
	r.wait(t, 1)
	// Two more turns: turnsSince is now 2, still under the (huge) turns interval.
	for i := 0; i < 2; i++ {
		_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "x"}}, &Step{})
	}
	r.assertNone(t)
}
