package agent

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// SubAgentMiddleware enforces sub-agent depth limits and lifecycle tracking.
type SubAgentMiddleware struct {
	MaxDepth int
	depth    int
}

func (m *SubAgentMiddleware) Name() string { return "sub_agent" }

func (m *SubAgentMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *SubAgentMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	if m.MaxDepth <= 0 {
		return step, nil
	}

	for _, tc := range msg.ToolCalls {
		if tc.Function.Name == "task" {
			if m.depth >= m.MaxDepth {
				msg.ToolCalls = removeTaskCalls(msg.ToolCalls)
				step.Messages = append(step.Messages, types.UserMsg(
					fmt.Sprintf("[system] Sub-agent depth limit (%d) reached — task tool call blocked.", m.MaxDepth),
				))
				return step, nil
			}
			m.depth++
		}
	}
	return step, nil
}

func (m *SubAgentMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}

func removeTaskCalls(calls []types.ToolCall) []types.ToolCall {
	filtered := make([]types.ToolCall, 0, len(calls))
	for _, tc := range calls {
		if tc.Function.Name != "task" {
			filtered = append(filtered, tc)
		}
	}
	return filtered
}
