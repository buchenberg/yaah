package pipeline

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestSubAgentMiddleware_BlocksBeyondDepthOne(t *testing.T) {
	m := &SubAgentMiddleware{}
	msg := &types.Message{ToolCalls: taskCallsN(3)}
	step := &Step{Messages: []types.Message{}}
	_, err := m.PostModel(context.Background(), msg, step)
	if err != nil {
		t.Fatalf("PostModel error: %v", err)
	}
	if got := len(msg.ToolCalls); got != 1 {
		t.Errorf("expected 1 task call retained (depth 1), got %d", got)
	}
}

func TestSubAgentMiddleware_NonTaskCallsPreserved(t *testing.T) {
	m := &SubAgentMiddleware{}
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Type: "function", Function: types.ToolCallFn{Name: "read", Arguments: `{}`}},
		{ID: "2", Type: "function", Function: types.ToolCallFn{Name: "spawn_subagent", Arguments: `{"prompt":"a"}`}},
		{ID: "3", Type: "function", Function: types.ToolCallFn{Name: "spawn_subagent", Arguments: `{"prompt":"b"}`}},
	}}
	step := &Step{Messages: []types.Message{}}
	m.PostModel(context.Background(), msg, step)
	if len(msg.ToolCalls) != 2 {
		t.Errorf("expected read + 1 task retained, got %d calls", len(msg.ToolCalls))
	}
}

func taskCallsN(n int) []types.ToolCall {
	calls := make([]types.ToolCall, n)
	for i := range calls {
		calls[i] = types.ToolCall{
			ID:       "c" + string(rune('1'+i)),
			Type:     "function",
			Function: types.ToolCallFn{Name: "spawn_subagent", Arguments: `{"description":"x","prompt":"y"}`},
		}
	}
	return calls
}
