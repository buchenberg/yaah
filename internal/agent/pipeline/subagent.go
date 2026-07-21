package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/types"
)

// SubAgentMiddleware enforces sub-agent depth limits and lifecycle tracking.
type SubAgentMiddleware struct {
	MaxDepth       int
	MaxDepthByRole map[subagent.SubAgentRole]int

	depth       int
	depthByRole map[subagent.SubAgentRole]int
}

func (m *SubAgentMiddleware) Name() string { return "sub_agent" }

func (m *SubAgentMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *SubAgentMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	if m.MaxDepth <= 0 && len(m.MaxDepthByRole) == 0 {
		return step, nil
	}
	if m.depthByRole == nil {
		m.depthByRole = make(map[subagent.SubAgentRole]int)
	}

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
		if tc.Function.Name != "task" {
			allowed = append(allowed, tc)
			continue
		}
		role := roleFromTaskArgs(tc.Function.Arguments)
		if m.roleBudgetExhausted(role) {
			blocked++
			continue
		}
		m.depth++
		m.depthByRole[role]++
		allowed = append(allowed, tc)
	}
	return allowed, blocked
}

func (m *SubAgentMiddleware) roleBudgetExhausted(role subagent.SubAgentRole) bool {
	if max, ok := m.MaxDepthByRole[role]; ok {
		return max > 0 && m.depthByRole[role] >= max
	}
	return m.MaxDepth > 0 && m.depth >= m.MaxDepth
}

func roleFromTaskArgs(args string) subagent.SubAgentRole {
	if args == "" {
		return subagent.RoleDefault
	}
	var parsed struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return subagent.RoleDefault
	}
	return subagent.SubAgentRole(parsed.Role)
}

func (m *SubAgentMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
