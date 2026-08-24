package pipeline

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// InlineLimitMiddleware caps the number of tool calls dispatched per
// turn. Dropped calls receive synthesized results via
// step.SynthesizedResults so every tool_call_id on the assistant
// message is answered. max <= 0 means unlimited.
type InlineLimitMiddleware struct{ max int }

func NewInlineLimitMiddleware(max int) *InlineLimitMiddleware {
	return &InlineLimitMiddleware{max: max}
}

func (m *InlineLimitMiddleware) Name() string { return "inline_limit" }

func (m *InlineLimitMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *InlineLimitMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	if m.max <= 0 || len(msg.ToolCalls) <= m.max {
		return step, nil
	}
	dropped := msg.ToolCalls[m.max:]
	msg.ToolCalls = msg.ToolCalls[:m.max]
	step.InlineDropped = len(dropped)
	for _, tc := range dropped {
		step.SynthesizedResults = append(step.SynthesizedResults, types.ToolResultMsg(
			tc.ID, tc.Function.Name,
			fmt.Sprintf(
				"[dropped: this call exceeded the inline tool limit (%d per turn) and was not executed. "+
					"Break large batches into smaller turns or use the delegate tool for batch work.]",
				m.max,
			),
		))
	}
	return step, nil
}

func (m *InlineLimitMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
