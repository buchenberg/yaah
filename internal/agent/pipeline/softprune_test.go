package pipeline

import (
	"context"
	"sync"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestSoftPrune_NoPruner_NoOp(t *testing.T) {
	m := &SoftPruneMiddleware{pruner: nil}
	step := &Step{Messages: []types.Message{types.UserMsg("hi")}}
	out, err := m.PostTool(context.Background(), nil, step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != step {
		t.Errorf("nil pruner should return the step unchanged")
	}
}

func TestSoftPrune_PostTool_Marks(t *testing.T) {
	pruner := NewPruner(PruneConfig{ProtectTokens: 1, MinReclaim: 1, MinTurns: 1})
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	msgs = append(msgs, turn("c1", "read", 5000)...)
	msgs = append(msgs, types.UserMsg("end"))
	step := &Step{Messages: msgs}
	// Snapshot the slice contents to prove PostTool does not mutate them.
	before := make([]types.Message, len(msgs))
	copy(before, msgs)

	m := &SoftPruneMiddleware{pruner: pruner}
	out, err := m.PostTool(context.Background(), nil, step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != step {
		t.Errorf("PostTool should return the same step reference (no mutation)")
	}
	if !pruner.IsPruned("c1") {
		t.Errorf("expected PostTool to mark the stale tool result")
	}
	// step.Messages must be byte-for-byte unchanged (constraint #2).
	for i := range before {
		if step.Messages[i].Content != before[i].Content {
			t.Errorf("PostTool mutated step.Messages[%d].Content", i)
		}
	}
}

func TestSoftPrune_EmitsHook(t *testing.T) {
	pruner := NewPruner(PruneConfig{ProtectTokens: 1, MinReclaim: 1, MinTurns: 1})
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	msgs = append(msgs, turn("c1", "read", 5000)...)
	msgs = append(msgs, types.UserMsg("end"))
	step := &Step{Messages: msgs}

	var (
		mu       sync.Mutex
		gotStats PruneStats
		called   bool
	)
	emit := func(s PruneStats) {
		mu.Lock()
		gotStats = s
		called = true
		mu.Unlock()
	}
	m := &SoftPruneMiddleware{pruner: pruner, emit: emit}
	if _, err := m.PostTool(context.Background(), nil, step); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Fatal("emit hook was not called")
	}
	if gotStats.Reason != "post_tool" {
		t.Errorf("emit stats reason = %q, want post_tool", gotStats.Reason)
	}
	if gotStats.Marked == 0 || !gotStats.Committed {
		t.Errorf("expected committed mark in emitted stats, got %+v", gotStats)
	}
}

func TestSoftPrune_OtelSpan_WhenHookSet(t *testing.T) {
	t.Run("otel hook invoked", func(t *testing.T) {
		pruner := NewPruner(PruneConfig{ProtectTokens: 1, MinReclaim: 1, MinTurns: 1})
		step := &Step{Messages: append([]types.Message{types.SystemMsg("sys")}, turn("c1", "read", 5000)...)}

		var called bool
		var mu sync.Mutex
		otel := func(ctx context.Context, s PruneStats) {
			mu.Lock()
			called = true
			mu.Unlock()
			if ctx == nil {
				t.Error("otel hook received nil ctx")
			}
		}
		m := &SoftPruneMiddleware{pruner: pruner, otel: otel}
		if _, err := m.PostTool(context.Background(), nil, step); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if !called {
			t.Error("otel hook was not called when set")
		}
	})

	t.Run("nil otel does not panic", func(t *testing.T) {
		pruner := NewPruner(PruneConfig{ProtectTokens: 1, MinReclaim: 1, MinTurns: 1})
		step := &Step{Messages: append([]types.Message{types.SystemMsg("sys")}, turn("c1", "read", 5000)...)}
		m := &SoftPruneMiddleware{pruner: pruner, otel: nil}
		if _, err := m.PostTool(context.Background(), nil, step); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestSoftPrune_BuilderWiring(t *testing.T) {
	// NewFromConfig must wire the pruner + hooks into the middleware when
	// soft_prune is in the resolved pipeline.
	pruner := NewPruner(DefaultPruneConfig())
	var emitCalled bool
	pipe := NewFromConfig(PipelineConfig{
		Pruner:     pruner,
		PruneHooks: PruneHooks{Emit: func(PruneStats) { emitCalled = true }},
	})
	names := pipe.MiddlewareNames()
	found := false
	for _, n := range names {
		if n == "soft_prune" {
			found = true
		}
	}
	if !found {
		t.Fatalf("soft_prune not in default pipeline: %v", names)
	}
	// Drive a PostTool through the assembled pipeline to confirm wiring.
	step := &Step{Messages: append([]types.Message{types.SystemMsg("sys")}, turn("c1", "read", 5000)...)}
	if _, err := pipe.RunPostTool(context.Background(), nil, step); err != nil {
		t.Fatalf("RunPostTool error: %v", err)
	}
	if !emitCalled {
		t.Error("emit hook was not wired through NewFromConfig")
	}
}

func TestSoftPrune_DisabledExcluded(t *testing.T) {
	pipe := NewFromConfig(PipelineConfig{
		PipelineDisabled: []string{"soft_prune"},
	})
	for _, n := range pipe.MiddlewareNames() {
		if n == "soft_prune" {
			t.Fatalf("soft_prune should be excluded by PipelineDisabled")
		}
	}
}
