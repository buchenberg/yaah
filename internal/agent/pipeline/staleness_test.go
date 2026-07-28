package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestStaleness_NoShift_NoAnnotation(t *testing.T) {
	m := &StalenessMiddleware{}
	step := &Step{}

	msg := &types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "tc1", Type: "function", Function: types.ToolCallFn{Name: "spawn_subagent", Arguments: `{}`}},
		},
	}
	_, _ = m.PostModel(context.Background(), msg, step)

	results := []ToolResult{
		{Name: "spawn_subagent", Result: "done"},
	}
	_, _ = m.PostTool(context.Background(), results, step)

	if strings.Contains(results[0].Result, "[staleness]") {
		t.Error("no context shift occurred; result should not be annotated")
	}
}

func TestStaleness_ContextShift_Annotates(t *testing.T) {
	m := &StalenessMiddleware{}
	step := &Step{}

	msg := &types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "tc1", Type: "function", Function: types.ToolCallFn{Name: "spawn_subagent", Arguments: `{}`}},
		},
	}
	_, _ = m.PostModel(context.Background(), msg, step)

	results := []ToolResult{
		{Name: "steer", Result: "user injected new direction"},
		{Name: "spawn_subagent", Result: "sub-agent output"},
	}
	_, _ = m.PostTool(context.Background(), results, step)

	if !strings.Contains(results[1].Result, "[staleness]") {
		t.Error("context shifted via steer; sub-agent result should be annotated")
	}
	if strings.Contains(results[0].Result, "[staleness]") {
		t.Error("steer result itself should not be annotated")
	}
}

func TestStaleness_NoSubAgent_NoOp(t *testing.T) {
	m := &StalenessMiddleware{}
	step := &Step{}

	msg := &types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "tc1", Type: "function", Function: types.ToolCallFn{Name: "read", Arguments: `{}`}},
		},
	}
	_, _ = m.PostModel(context.Background(), msg, step)

	results := []ToolResult{
		{Name: "steer", Result: "injected"},
		{Name: "read", Result: "file content"},
	}
	_, _ = m.PostTool(context.Background(), results, step)

	if strings.Contains(results[1].Result, "[staleness]") {
		t.Error("non-sub-agent results should never be annotated")
	}
}
