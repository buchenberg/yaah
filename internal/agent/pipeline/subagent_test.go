package pipeline

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/types"
)

func TestRoleFromTaskArgs(t *testing.T) {
	cases := []struct {
		args string
		want subagent.SubAgentRole
	}{
		{`{"role":"planner","prompt":"x"}`, subagent.RolePlanner},
		{`{"role":"planner"}`, subagent.RolePlanner},
		{`{"prompt":"x"}`, subagent.RoleDefault},
		{``, subagent.RoleDefault},
		{`{not valid json`, subagent.RoleDefault},
	}
	for _, c := range cases {
		if got := roleFromTaskArgs(c.args); got != c.want {
			t.Errorf("roleFromTaskArgs(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestSubAgentMiddleware_roleDepthEnforcement(t *testing.T) {
	t.Run("global MaxDepth blocks beyond limit", func(t *testing.T) {
		m := &SubAgentMiddleware{MaxDepth: 2}
		msg := &types.Message{ToolCalls: taskCallsN(3)}
		step := &Step{Messages: []types.Message{}}
		_, err := m.PostModel(context.Background(), msg, step)
		if err != nil {
			t.Fatalf("PostModel error: %v", err)
		}
		if got := len(msg.ToolCalls); got != 2 {
			t.Errorf("expected 2 task calls retained, got %d", got)
		}
	})

	t.Run("per-role limit", func(t *testing.T) {
		m := &SubAgentMiddleware{
			MaxDepthByRole: map[subagent.SubAgentRole]int{subagent.RolePlanner: 1},
		}
		calls := []types.ToolCall{
			{ID: "1", Type: "function", Function: types.ToolCallFn{Name: "task", Arguments: `{"role":"planner","prompt":"a"}`}},
			{ID: "2", Type: "function", Function: types.ToolCallFn{Name: "task", Arguments: `{"role":"planner","prompt":"b"}`}},
		}
		msg := &types.Message{ToolCalls: calls}
		step := &Step{Messages: []types.Message{}}
		_, err := m.PostModel(context.Background(), msg, step)
		if err != nil {
			t.Fatalf("PostModel error: %v", err)
		}
		if got := len(msg.ToolCalls); got != 1 {
			t.Errorf("expected 1 planner call retained, got %d", got)
		}
	})

	t.Run("non-task calls preserved", func(t *testing.T) {
		m := &SubAgentMiddleware{MaxDepth: 1}
		msg := &types.Message{ToolCalls: []types.ToolCall{
			{ID: "1", Type: "function", Function: types.ToolCallFn{Name: "read", Arguments: `{}`}},
			{ID: "2", Type: "function", Function: types.ToolCallFn{Name: "task", Arguments: `{"prompt":"a"}`}},
			{ID: "3", Type: "function", Function: types.ToolCallFn{Name: "task", Arguments: `{"prompt":"b"}`}},
		}}
		step := &Step{Messages: []types.Message{}}
		m.PostModel(context.Background(), msg, step)
		if len(msg.ToolCalls) != 2 {
			t.Errorf("expected read + 1 task retained, got %d calls", len(msg.ToolCalls))
		}
	})

	t.Run("disabled when no limits set", func(t *testing.T) {
		m := &SubAgentMiddleware{}
		msg := &types.Message{ToolCalls: taskCallsN(5)}
		step := &Step{Messages: []types.Message{}}
		m.PostModel(context.Background(), msg, step)
		if len(msg.ToolCalls) != 5 {
			t.Errorf("with no limits, all calls should pass, got %d", len(msg.ToolCalls))
		}
	})
}

func taskCallsN(n int) []types.ToolCall {
	calls := make([]types.ToolCall, n)
	for i := range calls {
		calls[i] = types.ToolCall{
			ID:       "c" + string(rune('1'+i)),
			Type:     "function",
			Function: types.ToolCallFn{Name: "task", Arguments: `{"description":"x","prompt":"y"}`},
		}
	}
	return calls
}
