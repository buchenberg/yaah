package pipeline

import (
	"fmt"
	"sync"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// bigToolResult builds a tool-result message with enough content to exceed
// a given token estimate (tokens * 4 chars).
func bigToolResult(id, name string, tokens int) types.Message {
	return types.ToolResultMsg(id, name, fmt.Sprintf("x-%s-%s", id, repeat("x", tokens*4)))
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		b = append(b, s...)
	}
	return string(b)
}

// turn builds a minimal user/assistant/tool triple for one tool call.
func turn(callID, toolName string, tokens int) []types.Message {
	return []types.Message{
		types.UserMsg("do something"),
		types.AssistantMsg("", []types.ToolCall{{
			ID:       callID,
			Type:     "function",
			Function: types.ToolCallFn{Name: toolName, Arguments: "{}"},
		}}),
		bigToolResult(callID, toolName, tokens),
	}
}

func TestPruner_ProtectWindow(t *testing.T) {
	// ProtectTokens high enough that nothing exceeds it → nothing marked.
	p := NewPruner(PruneConfig{
		ProtectTokens: 1_000_000,
		MinReclaim:    1,
		MinTurns:      1,
	})
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	msgs = append(msgs, turn("c1", "read", 1000)...)
	msgs = append(msgs, types.UserMsg("again"))
	msgs = append(msgs, turn("c2", "read", 1000)...)
	msgs = append(msgs, types.UserMsg("end"))

	stats := p.Mark(msgs, "post_tool")
	if stats.Committed {
		t.Fatalf("expected no commit within protect window, got %+v", stats)
	}
	if stats.Marked != 0 {
		t.Errorf("expected 0 marked, got %d", stats.Marked)
	}
	if p.IsPruned("c1") || p.IsPruned("c2") {
		t.Errorf("nothing should be pruned within protect window")
	}
}

func TestPruner_MarksBeyondWindow(t *testing.T) {
	// Protect window large enough to hold exactly one 500-token result, so
	// the most recent is shielded while older ones exceed the window.
	p := NewPruner(PruneConfig{
		ProtectTokens: 600,
		MinReclaim:    10,
		MinTurns:      1,
	})
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	// several turns; older tool results exceed the protect window
	msgs = append(msgs, turn("c1", "read", 500)...)
	msgs = append(msgs, types.UserMsg("u2"))
	msgs = append(msgs, turn("c2", "read", 500)...)
	msgs = append(msgs, types.UserMsg("u3"))
	msgs = append(msgs, turn("c3", "read", 500)...)
	msgs = append(msgs, types.UserMsg("end"))

	stats := p.Mark(msgs, "post_tool")
	if !stats.Committed {
		t.Fatalf("expected commit, got %+v", stats)
	}
	if stats.Marked == 0 {
		t.Fatalf("expected at least one marked beyond window, got %+v", stats)
	}
	// The most recent tool (c3, 500 tokens) fits within the 600-token
	// protect window → not pruned. c1 and c2 push total past the window.
	if p.IsPruned("c3") {
		t.Errorf("most recent tool result should be protected, but was pruned: %+v", stats)
	}
	if !p.IsPruned("c1") {
		t.Errorf("oldest tool result should be pruned: %+v", stats)
	}
}

// TestPruner_MinReclaim: candidates exist but reclaim < MinReclaim → not committed.
func TestPruner_MinReclaim(t *testing.T) {
	p := NewPruner(PruneConfig{
		ProtectTokens: 100,
		MinReclaim:    1_000_000, // impossibly high
		MinTurns:      1,
	})
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	msgs = append(msgs, turn("c1", "read", 500)...)
	msgs = append(msgs, types.UserMsg("end"))

	stats := p.Mark(msgs, "post_tool")
	if stats.Candidates == 0 {
		t.Fatalf("expected candidates to exist, got %+v", stats)
	}
	if stats.Committed {
		t.Errorf("should not commit when reclaim < MinReclaim, got %+v", stats)
	}
	if p.IsPruned("c1") {
		t.Errorf("should not prune when not committed")
	}
}

func TestPruner_MinTurns(t *testing.T) {
	// The last MinTurns turn-units (user messages OR assistant messages with
	// tool calls) are never marked. With MinTurns=2 and the new counting,
	// the last iteration's user + assistant(tool_calls) = 2 units are
	// protected. Older iterations' tool results are eligible.
	p := NewPruner(PruneConfig{
		ProtectTokens: 1, // tiny so everything would otherwise be eligible
		MinReclaim:    1,
		MinTurns:      2,
	})
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	// turn 1 (oldest)
	msgs = append(msgs, types.UserMsg("u1"))
	msgs = append(msgs, turn("c1", "read", 5000)[1:]...) // drop the turn()'s leading user
	// turn 2
	msgs = append(msgs, types.UserMsg("u2"))
	msgs = append(msgs, turn("c2", "read", 5000)[1:]...)
	// turn 3 (most recent)
	msgs = append(msgs, types.UserMsg("u3"))
	msgs = append(msgs, turn("c3", "read", 5000)[1:]...)

	stats := p.Mark(msgs, "post_tool")
	// MinTurns=2 protects the last 2 turn-units: user("u3") + assistant(c3).
	// Turns 1 and 2 tool results are eligible → 2 marked.
	if stats.Marked != 2 {
		t.Errorf("expected 2 marked (turns 1 and 2), got %d: %+v", stats.Marked, stats)
	}
	if !p.IsPruned("c1") || !p.IsPruned("c2") {
		t.Errorf("older turns' tool results should be pruned: c1=%v c2=%v", p.IsPruned("c1"), p.IsPruned("c2"))
	}
	if p.IsPruned("c3") {
		t.Errorf("most recent turn's tool result must be protected by MinTurns=2")
	}
}

func TestPruner_ProtectedTools(t *testing.T) {
	p := NewPruner(PruneConfig{
		ProtectTokens:  1,
		MinReclaim:     1,
		MinTurns:       1,
		ProtectedTools: map[string]bool{"skill": true},
	})
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	msgs = append(msgs, turn("c1", "skill", 5000)...) // protected
	msgs = append(msgs, turn("c2", "read", 5000)...)  // eligible
	msgs = append(msgs, types.UserMsg("end"))

	stats := p.Mark(msgs, "post_tool")
	if stats.ProtectedSkipped == 0 {
		t.Errorf("expected the skill tool result to be skipped as protected")
	}
	if p.IsPruned("c1") {
		t.Errorf("skill tool result must never be pruned")
	}
	if !p.IsPruned("c2") {
		t.Errorf("read tool result beyond window should be pruned")
	}
}

func TestPruner_AlreadyPrunedBreaks(t *testing.T) {
	// Pre-mark the oldest tool; the walk must stop at it and not visit
	// (or re-mark) anything older. We detect over-walk by placing a
	// sentinel very-old tool result BEFORE the pre-marked one and
	// confirming it is NOT added.
	p := NewPruner(PruneConfig{
		ProtectTokens: 1,
		MinReclaim:    1,
		MinTurns:      1,
	})
	// manually pre-mark c_old so the walk hits the boundary
	p.pruned["c_boundary"] = true

	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	msgs = append(msgs, turn("c_sentinel", "read", 5000)...) // older than boundary; must NOT be marked
	msgs = append(msgs, types.UserMsg("u"))
	msgs = append(msgs, turn("c_boundary", "read", 5000)...) // already pruned → walk stops here
	msgs = append(msgs, types.UserMsg("end"))

	stats := p.Mark(msgs, "post_tool")
	if p.IsPruned("c_sentinel") {
		t.Errorf("walk should stop at already-pruned boundary; sentinel was marked: %+v", stats)
	}
}

func TestPruner_SummaryBoundary(t *testing.T) {
	// A non-index-0 system message (compaction summary) terminates the walk.
	p := NewPruner(PruneConfig{
		ProtectTokens: 1,
		MinReclaim:    1,
		MinTurns:      1,
	})
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("real system prompt")) // index 0 — not a boundary
	msgs = append(msgs, turn("c_old", "read", 5000)...)
	msgs = append(msgs, types.SystemMsg("Previous conversation summary: ...")) // boundary
	msgs = append(msgs, types.UserMsg("after summary"))
	msgs = append(msgs, turn("c_new", "read", 5000)...)
	msgs = append(msgs, types.UserMsg("end"))

	stats := p.Mark(msgs, "post_tool")
	if p.IsPruned("c_old") {
		t.Errorf("tool result before summary boundary must not be pruned: %+v", stats)
	}
}

func TestPruner_Filter_StubsMarkedOnly(t *testing.T) {
	p := NewPruner(PruneConfig{
		ProtectTokens: 1,
		MinReclaim:    1,
		MinTurns:      1,
	})
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	msgs = append(msgs, turn("c_old", "read", 5000)...)
	msgs = append(msgs, types.UserMsg("u"))
	msgs = append(msgs, turn("c_new", "read", 5000)...)
	msgs = append(msgs, types.UserMsg("end"))

	p.Mark(msgs, "post_tool")

	out := p.Filter(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("Filter changed message count: %d vs %d", len(out), len(msgs))
	}
	for i, m := range out {
		orig := msgs[i]
		if m.Role != orig.Role {
			t.Errorf("msg[%d] role changed: %q vs %q", i, m.Role, orig.Role)
		}
		if m.Name != orig.Name {
			t.Errorf("msg[%d] name changed: %q vs %q", i, m.Name, orig.Name)
		}
		if m.ToolCallID != orig.ToolCallID {
			t.Errorf("msg[%d] tool_call_id changed: %q vs %q", i, m.ToolCallID, orig.ToolCallID)
		}
		if m.Role == "tool" && p.IsPruned(m.ToolCallID) {
			if m.Content == orig.Content {
				t.Errorf("pruned tool %q content not stubbed", m.ToolCallID)
			}
		}
	}
}

func TestPruner_Filter_FastPath(t *testing.T) {
	p := NewPruner(DefaultPruneConfig())
	msgs := []types.Message{types.SystemMsg("sys"), types.UserMsg("hi")}
	out := p.Filter(msgs)
	// Nothing pruned → must return the same slice header (no allocation).
	if &out[0] != &msgs[0] {
		t.Errorf("fast path should return the input slice unchanged (same backing array)")
	}
}

func TestPruner_Filter_DoesNotMutateInput(t *testing.T) {
	p := NewPruner(PruneConfig{ProtectTokens: 1, MinReclaim: 1, MinTurns: 1})
	msgs := []types.Message{types.SystemMsg("sys")}
	msgs = append(msgs, turn("c1", "read", 5000)...)
	msgs = append(msgs, types.UserMsg("end"))
	p.Mark(msgs, "post_tool")

	origContent := ""
	for _, m := range msgs {
		if m.ToolCallID == "c1" {
			origContent = m.Content
		}
	}
	_ = p.Filter(msgs)

	for _, m := range msgs {
		if m.ToolCallID == "c1" && m.Content != origContent {
			t.Errorf("Filter mutated the input slice content for c1")
		}
	}
}

func TestPruner_Reset(t *testing.T) {
	p := NewPruner(PruneConfig{ProtectTokens: 1, MinReclaim: 1, MinTurns: 1})
	msgs := []types.Message{types.SystemMsg("sys")}
	msgs = append(msgs, turn("c1", "read", 5000)...)
	msgs = append(msgs, types.UserMsg("end"))
	stats := p.Mark(msgs, "post_tool")
	if stats.Marked == 0 {
		t.Fatalf("precondition: expected something marked")
	}
	cumBefore := p.Stats().CumMarked

	p.Reset()

	if p.IsPruned("c1") {
		t.Errorf("Reset should clear the pruned set")
	}
	if s := p.Stats(); s.TotalMarked != 0 {
		t.Errorf("TotalMarked after reset should be 0, got %d", s.TotalMarked)
	} else if s.CumMarked != cumBefore {
		t.Errorf("cumulative marked should survive reset: got %d want %d", s.CumMarked, cumBefore)
	}
	// Filter after reset is identity (fast path).
	out := p.Filter(msgs)
	if &out[0] != &msgs[0] {
		t.Errorf("Filter after Reset should be identity")
	}
}

func TestPruner_ConcurrentSafe(t *testing.T) {
	p := NewPruner(PruneConfig{ProtectTokens: 1, MinReclaim: 1, MinTurns: 1})
	msgs := []types.Message{types.SystemMsg("sys")}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, turn(fmt.Sprintf("c%d", i), "read", 500)...)
		msgs = append(msgs, types.UserMsg("u"))
	}
	msgs = append(msgs, types.UserMsg("end"))

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Mark(msgs, "post_tool")
			_ = p.Filter(msgs)
			_ = p.Stats()
		}()
	}
	wg.Wait()
	// No panic / race under -race is the assertion.
}

// TestPruner_DefaultsPruneRealisticVolume reproduces the production slowdown:
// a realistic session accumulates many moderate tool results (~25k tokens
// across 10 turns). With the shipped defaults the pruner must actually commit
// a prune — if it does not (candidates=0 / reclaimed=0 every call, as seen
// across 407 prune spans in real traces), context grows unbounded. This test
// pins the defaults low enough that realistic volume triggers a prune.
func TestPruner_DefaultsPruneRealisticVolume(t *testing.T) {
	p := NewPruner(DefaultPruneConfig())
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	for i := 0; i < 10; i++ {
		msgs = append(msgs, turn(fmt.Sprintf("c%d", i), "read", 2500)...)
	}
	msgs = append(msgs, types.UserMsg("end"))

	stats := p.Mark(msgs, "post_tool")
	if !stats.Committed {
		t.Fatalf("pruner should commit on realistic tool volume with defaults, got %+v", stats)
	}
	if stats.ReclaimedTokens == 0 {
		t.Fatalf("pruner should reclaim tokens with defaults, got %+v", stats)
	}
	if stats.Marked == 0 {
		t.Fatalf("expected at least one tool result marked, got %+v", stats)
	}
}

// TestPruner_SinglePromptMultiIteration reproduces the TUI/one-shot bug:
// a session with ONE user message followed by many assistant+tool iterations.
// The old MinTurns logic counted only user messages, so the pruner never
// activated (turns never reached 2). The fix counts assistant messages with
// tool calls as turns too.
func TestPruner_SinglePromptMultiIteration(t *testing.T) {
	p := NewPruner(DefaultPruneConfig())
	var msgs []types.Message
	msgs = append(msgs, types.SystemMsg("sys"))
	msgs = append(msgs, types.UserMsg("do the task"))
	// Simulate 8 iterations: assistant(tool_calls) + tool_result, no new user msgs
	for i := 0; i < 8; i++ {
		msgs = append(msgs, types.AssistantMsg("", []types.ToolCall{{
			ID:       fmt.Sprintf("c%d", i),
			Type:     "function",
			Function: types.ToolCallFn{Name: "read", Arguments: "{}"},
		}}))
		msgs = append(msgs, bigToolResult(fmt.Sprintf("c%d", i), "read", 2500))
	}

	stats := p.Mark(msgs, "post_tool")
	if stats.Candidates == 0 {
		t.Fatalf("single-prompt multi-iteration session must produce candidates, got %+v", stats)
	}
	if !stats.Committed {
		t.Fatalf("pruner should commit in single-prompt session, got %+v", stats)
	}
	if stats.ReclaimedTokens == 0 {
		t.Fatalf("pruner should reclaim tokens, got %+v", stats)
	}
}

func TestPruner_DefaultConfig(t *testing.T) {
	p := NewPruner(PruneConfig{}) // all zero → defaults applied
	if p.cfg.ProtectTokens != defaultPruneProtectTokens {
		t.Errorf("ProtectTokens default not applied: %d", p.cfg.ProtectTokens)
	}
	if p.cfg.MinReclaim != defaultPruneMinReclaim {
		t.Errorf("MinReclaim default not applied: %d", p.cfg.MinReclaim)
	}
	if p.cfg.MinTurns != defaultPruneMinTurns {
		t.Errorf("MinTurns default not applied: %d", p.cfg.MinTurns)
	}
	if !p.cfg.ProtectedTools["skill"] {
		t.Errorf("ProtectedTools default should include 'skill'")
	}
}
