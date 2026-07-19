package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// SubAgentMiddleware enforces sub-agent depth limits and lifecycle tracking.
//
// Two depth mechanisms are supported:
//
//  1. A legacy cumulative MaxDepth that caps the total number of task
//     calls a single Loop may issue across its lifetime.
//  2. An optional per-role MaxDepthByRole map that caps task calls per
//     role independently. This lets a planner spawn workers freely while
//     keeping runaway reviewer or worker dispatch in check.
//
// Actual nesting depth (planner → worker → ...) is bounded structurally
// by the tool profiles: only the planner role registers the task tool,
// and makeTaskRunner decrements the remaining depth on each level so a
// sub-loop eventually loses its task tool entirely.
type SubAgentMiddleware struct {
	// MaxDepth caps total task calls for this Loop. 0 means unlimited.
	MaxDepth int

	// MaxDepthByRole optionally caps task calls per role. A role absent
	// from the map falls back to MaxDepth. 0 means unlimited for that
	// role.
	MaxDepthByRole map[SubAgentRole]int

	// depth is the cumulative task-call counter for this Loop.
	depth int

	// depthByRole tracks task calls per role for this Loop.
	depthByRole map[SubAgentRole]int
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
		m.depthByRole = make(map[SubAgentRole]int)
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

// filterTaskCalls walks the tool calls, allowing task calls that are
// still within their per-role (or global) depth budget and dropping the
// rest. Non-task calls are always preserved. The depth counters are
// incremented for each allowed task call.
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

// roleBudgetExhausted returns true if the role has used up its allowed
// number of task calls. A role with no explicit per-role limit falls
// back to the global MaxDepth.
func (m *SubAgentMiddleware) roleBudgetExhausted(role SubAgentRole) bool {
	if max, ok := m.MaxDepthByRole[role]; ok {
		return max > 0 && m.depthByRole[role] >= max
	}
	return m.MaxDepth > 0 && m.depth >= m.MaxDepth
}

// roleFromTaskArgs extracts the role from a task tool call's JSON
// arguments. Returns RoleDefault when absent or unparseable.
func roleFromTaskArgs(args string) SubAgentRole {
	if args == "" {
		return RoleDefault
	}
	var parsed struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return RoleDefault
	}
	return SubAgentRole(parsed.Role)
}

func (m *SubAgentMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
