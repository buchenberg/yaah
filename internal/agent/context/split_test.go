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
