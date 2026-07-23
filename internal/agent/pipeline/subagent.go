package pipeline

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// SubAgentMiddleware enforces sub-agent depth limits (hardcoded to 1).
type SubAgentMiddleware struct {
	depth int
}

func (m *SubAgentMiddleware) Name() string { return "sub_agent" }

func (m *SubAgentMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *SubAgentMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	filtered, blocked := m.filterTaskCalls(msg.ToolCalls)
	if blocked > 0 {
		msg.ToolCalls = filtered
		step.Messages = append(step.Messages, types.UserMsg(
			fmt.Sprintf("[system] Sub-agent depth limit reached — %d task tool call(s) blocked.", blocked),
		))
	}
	return step, nil
}

func (m *SubAgentMiddleware) filterTaskCalls(calls []types.ToolCall) (allowed []types.ToolCall, blocked int) {
	allowed = make([]types.ToolCall, 0, len(calls))
	for _, tc := range calls {
		if tc.Function.Name != "spawn_subagent" {
			allowed = append(allowed, tc)
			continue
		}
		if m.depth >= 1 {
			blocked++
			continue
		}
		m.depth++
		allowed = append(allowed, tc)
	}
	return allowed, blocked
}

func (m *SubAgentMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
