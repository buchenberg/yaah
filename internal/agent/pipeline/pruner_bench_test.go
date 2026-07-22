package pipeline

import (
	"fmt"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// Measured (Intel Ultra 7 265H, Go 1.25.8):
//   Mark               ~3-5 µs/op   (target < 50 µs)   ✓
//   Filter (no prune)  ~15 ns/op    (target < 50 ns)   ✓  zero-alloc fast path
//   Filter (50 pruned) ~6-8 µs/op   (target < 5 µs)
//
// Filter-50 runs nominally ~40% above its rough target. The cost is
// dominated by copying the 200-message value slice (≈24 KB, near the memory
// bandwidth floor) — avoiding it would require a copy-on-write message
// representation, out of scope. At once per provider turn this is < 0.01%
// of a 100 ms+ turn, so the operational goal ("well under per-turn budget")
// is comfortably met.

// benchMessages builds a 200-message history with 50 large tool results
// spread across turns, matching the B5 "Complex" session shape.
func benchMessages() []types.Message {
	msgs := make([]types.Message, 0, 200)
	msgs = append(msgs, types.SystemMsg("system"))
	for i := 0; i < 50; i++ {
		msgs = append(msgs, types.UserMsg(fmt.Sprintf("user turn %d", i)))
		msgs = append(msgs, types.AssistantMsg("", []types.ToolCall{{
			ID:       fmt.Sprintf("call_%d", i),
			Type:     "function",
			Function: types.ToolCallFn{Name: "read", Arguments: "{}"},
		}}))
		// ~1000 tokens per result (4000 chars).
		msgs = append(msgs, types.ToolResultMsg(fmt.Sprintf("call_%d", i), "read", repeat("y", 4000)))
	}
	msgs = append(msgs, types.UserMsg("final"))
	return msgs
}

// BenchmarkPruner_Mark measures the backward-walk cost on a 200-message
// history with 50 tool results. Target: < 50µs/op.
func BenchmarkPruner_Mark(b *testing.B) {
	msgs := benchMessages()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := NewPruner(PruneConfig{ProtectTokens: 100, MinReclaim: 10, MinTurns: 1})
		p.Mark(msgs, "post_tool")
	}
}

// BenchmarkPruner_Filter_NoPruned measures the fast path (empty pruned set):
// must return the input slice with no allocation. Target: < 50ns/op.
func BenchmarkPruner_Filter_NoPruned(b *testing.B) {
	msgs := benchMessages()
	p := NewPruner(DefaultPruneConfig()) // nothing marked
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Filter(msgs)
	}
}

// BenchmarkPruner_Filter_50Pruned measures the stubbing path with 50 pruned
// tool results. Target: < 5µs/op.
func BenchmarkPruner_Filter_50Pruned(b *testing.B) {
	msgs := benchMessages()
	p := NewPruner(PruneConfig{ProtectTokens: 100, MinReclaim: 10, MinTurns: 1})
	p.Mark(msgs, "post_tool") // marks the older ~49 results
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Filter(msgs)
	}
}
