package context

import (
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func msg(role, content string, calls ...types.ToolCall) types.Message {
	return types.Message{Role: role, Content: content, ToolCalls: calls}
}

func TestSplitTailKeepsFinalExchangeInOversizedTurn(t *testing.T) {
	call := types.ToolCall{ID: "c1", Function: types.ToolCallFn{Name: "read", Arguments: "{}"}}
	long := make([]byte, 4000)
	for i := range long {
		long[i] = 'x'
	}
	messages := []types.Message{
		msg("system", "sys"),
		msg("user", string(long)),
		msg("assistant", "", call),
		msg("tool", string(long)),
		msg("assistant", "", types.ToolCall{ID: "c2", Function: types.ToolCallFn{Name: "read", Arguments: "{}"}}),
		msg("tool", string(long)),
	}
	split := SplitTail(messages, 10)
	if split.KeepStart <= 1 || split.KeepStart >= len(messages) {
		t.Fatalf("KeepStart = %d; want an interior split with something to summarize", split.KeepStart)
	}
	if messages[split.KeepStart].Role == "tool" {
		t.Fatalf("split leaves a tool result without its assistant call at %d", split.KeepStart)
	}
}

func TestSplitTailNormalBudget(t *testing.T) {
	messages := []types.Message{
		msg("system", "sys"),
		msg("user", "one"),
		msg("assistant", "two"),
		msg("user", "three"),
		msg("assistant", "four"),
	}
	split := SplitTail(messages, 1000)
	if split.KeepStart != 1 {
		t.Fatalf("KeepStart = %d, want 1 (all turns fit)", split.KeepStart)
	}
}

func reasoningAssistant(content string) types.Message {
	return types.Message{Role: "assistant", Content: content, ReasoningContent: "thinking"}
}

func TestProtectReasoningTurnsLimitsWindowToNewestCarriers(t *testing.T) {
	messages := []types.Message{
		msg("system", "sys"),
		msg("user", "task"),
		reasoningAssistant("a-old"), // head carrier
		msg("tool", "r1"),
		reasoningAssistant("a-mid"),
		msg("tool", "r2"),
		reasoningAssistant("a-new"),
		msg("tool", "r3"),
	}

	// Cut past the head carrier: the windowed rule keeps the two newest
	// carriers and releases the head one — the single-turn collapse fix
	// (yaah-6a6c).
	if got := ProtectReasoningTurns(messages, 5, 2); got != 4 {
		t.Errorf("keepStart = %d, want 4 (protect the two newest carriers, release the head one)", got)
	}
	// A cut that already satisfies the window passes through unchanged.
	if got := ProtectReasoningTurns(messages, 4, 2); got != 4 {
		t.Errorf("keepStart = %d, want 4 (unchanged)", got)
	}
	// Fewer carriers than the window: protect them all, rewinding to the
	// oldest carrier's turn start — the user prompt that opened the turn.
	if got := ProtectReasoningTurns(messages, 6, 5); got != 1 {
		t.Errorf("keepStart = %d, want 1 (protect every carrier when fewer than protectTurns exist; the turn start is the opening user prompt)", got)
	}
	// No reasoning at all: no adjustment.
	plain := []types.Message{msg("system", "sys"), msg("user", "u"), msg("tool", "r"), msg("tool", "r2")}
	if got := ProtectReasoningTurns(plain, 2, 2); got != 2 {
		t.Errorf("keepStart = %d, want 2 (no carriers, no protection)", got)
	}
}

func TestSnapCutPastToolResults(t *testing.T) {
	messages := []types.Message{
		msg("system", "sys"),
		reasoningAssistant("caller"), // carries calls for the tool results below
		msg("tool", "r1"),
		msg("tool", "r2"),
		reasoningAssistant("next"),
	}

	call := types.ToolCall{ID: "c1", Function: types.ToolCallFn{Name: "read", Arguments: "{}"}}
	multi := []types.Message{
		msg("system", "sys"),
		{Role: "assistant", Content: "caller", ToolCalls: []types.ToolCall{call, call, call}},
		msg("tool", "r1"),
		msg("tool", "r2"),
		msg("tool", "r3"),
		reasoningAssistant("boundary"),
	}
	if got := SnapCutPastToolResults(multi, 2); got != 5 {
		t.Errorf("keepStart = %d, want 5 (cutting between a caller and its tool results orphans them — snap to the next exchange)", got)
	}
	if got := SnapCutPastToolResults(messages, 2); got != 4 {
		t.Errorf("keepStart = %d, want 4 (both tool results skipped to the next assistant)", got)
	}
	if got := SnapCutPastToolResults(messages, 4); got != 4 {
		t.Errorf("keepStart = %d, want 4 (assistant boundaries pass through)", got)
	}
}
