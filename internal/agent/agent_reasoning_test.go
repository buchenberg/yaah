package agent

import (
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func assistantWithReasoning(content, reasoning string) types.Message {
	return types.Message{Role: "assistant", Content: content, ReasoningContent: reasoning}
}

func TestStripOldReasoning_stripsOldPreservesRecent(t *testing.T) {
	msgs := []types.Message{
		types.SystemMsg("sys"),                   // idx 0
		types.UserMsg("u1"),                      // idx 1
		assistantWithReasoning("a1", "reason-1"), // idx 2 — old, stripped
		types.UserMsg("u2"),                      // idx 3
		assistantWithReasoning("a2", "reason-2"), // idx 4 — recent, kept
		types.UserMsg("u3"),                      // idx 5
		assistantWithReasoning("a3", "reason-3"), // idx 6 — recent, kept
	}

	out := stripOldReasoning(msgs, 2)

	if out[2].ReasoningContent != "" {
		t.Errorf("old reasoning (idx 2) should be stripped, got %q", out[2].ReasoningContent)
	}
	if out[4].ReasoningContent != "reason-2" {
		t.Errorf("recent reasoning (idx 4) should be preserved, got %q", out[4].ReasoningContent)
	}
	if out[6].ReasoningContent != "reason-3" {
		t.Errorf("recent reasoning (idx 6) should be preserved, got %q", out[6].ReasoningContent)
	}
}

func TestStripOldReasoning_doesNotMutateInput(t *testing.T) {
	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("u1"),
		assistantWithReasoning("a1", "reason-1"),
		types.UserMsg("u2"),
		assistantWithReasoning("a2", "reason-2"),
		types.UserMsg("u3"),
		assistantWithReasoning("a3", "reason-3"),
	}

	_ = stripOldReasoning(msgs, 2)

	if msgs[2].ReasoningContent != "reason-1" {
		t.Errorf("input message mutated: idx 2 reasoning = %q, want %q", msgs[2].ReasoningContent, "reason-1")
	}
}

func TestStripOldReasoning_zeroAllocFastPath(t *testing.T) {
	// No reasoning content anywhere -> input slice returned unchanged.
	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("u1"),
		types.AssistantMsg("a1", nil),
		types.UserMsg("u2"),
		types.AssistantMsg("a2", nil),
		types.UserMsg("u3"),
		types.AssistantMsg("a3", nil),
	}

	out := stripOldReasoning(msgs, 2)

	if &out[0] != &msgs[0] {
		t.Error("expected zero-alloc fast path to return the same backing array")
	}
}

func TestStripOldReasoning_fewerTurnsThanProtect(t *testing.T) {
	// Only one user turn but protectTurns=2 -> nothing is old enough to strip.
	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("u1"),
		assistantWithReasoning("a1", "reason-1"),
	}

	out := stripOldReasoning(msgs, 2)

	if out[2].ReasoningContent != "reason-1" {
		t.Errorf("reasoning should be preserved when fewer turns than protect window, got %q", out[2].ReasoningContent)
	}
}

func TestStripOldReasoning_toolLinkageUntouched(t *testing.T) {
	// Stripping reasoning must not remove messages or disturb tool-call/result
	// pairing — it only clears the ReasoningContent field.
	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("u1"),
		types.Message{
			Role:             "assistant",
			ReasoningContent: "old-reason",
			ToolCalls: []types.ToolCall{{
				ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "read"},
			}},
		},
		{Role: "tool", ToolCallID: "c1", Name: "read", Content: "result"},
		types.UserMsg("u2"),
		types.UserMsg("u3"),
	}

	out := stripOldReasoning(msgs, 2)

	// Message count unchanged.
	if len(out) != len(msgs) {
		t.Fatalf("message count changed: before=%d after=%d", len(msgs), len(out))
	}
	// Reasoning cleared but tool call preserved.
	if out[2].ReasoningContent != "" {
		t.Errorf("reasoning should be stripped, got %q", out[2].ReasoningContent)
	}
	if len(out[2].ToolCalls) != 1 || out[2].ToolCalls[0].ID != "c1" {
		t.Errorf("tool call lost during reasoning strip: %#v", out[2].ToolCalls)
	}
	// Tool result still present and linked.
	if out[3].Role != "tool" || out[3].ToolCallID != "c1" {
		t.Errorf("tool result lost during reasoning strip: %#v", out[3])
	}
}

func TestMessageTokens_countsReasoning(t *testing.T) {
	withoutReasoning := types.Message{Role: "assistant", Content: ""}
	withReasoning := types.Message{Role: "assistant", Content: "", ReasoningContent: repeatChars(400)}

	base := messageTokens(withoutReasoning)
	withR := messageTokens(withReasoning)

	// 400 chars of reasoning ≈ 100 tokens; the no-reasoning message sits at the
	// 10-token floor, so the reasoning-bearing message must be far larger.
	if withR <= base {
		t.Errorf("messageTokens should count reasoning: without=%d with=%d", base, withR)
	}
	if withR != 100 {
		t.Errorf("messageTokens with 400 chars reasoning = %d, want 100", withR)
	}
}

func repeatChars(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestPrepareRequestMessages_stripsReasoningAndPreservesHistory(t *testing.T) {
	loop := &Loop{ReasoningProtectTurns: 2}
	loop.ensurePruner()

	msgs := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("u1"),
		assistantWithReasoning("a1", "reason-1"),
		types.UserMsg("u2"),
		assistantWithReasoning("a2", "reason-2"),
		types.UserMsg("u3"),
		assistantWithReasoning("a3", "reason-3"),
	}

	out := loop.prepareRequestMessages(msgs)

	if out[2].ReasoningContent != "" {
		t.Errorf("prepareRequestMessages should strip old reasoning, got %q", out[2].ReasoningContent)
	}
	if out[4].ReasoningContent != "reason-2" {
		t.Errorf("prepareRequestMessages should preserve recent reasoning, got %q", out[4].ReasoningContent)
	}
	// Stored history must be untouched.
	if msgs[2].ReasoningContent != "reason-1" {
		t.Errorf("stored history mutated: idx 2 reasoning = %q", msgs[2].ReasoningContent)
	}
}
