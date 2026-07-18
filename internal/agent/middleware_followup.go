package agent

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// FollowupMiddleware drains queued follow-up messages between turns.
type FollowupMiddleware struct {
	ch <-chan string
}

func (m *FollowupMiddleware) Name() string { return "followup" }

func (m *FollowupMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	if m.ch == nil {
		return step, nil
	}
	select {
	case msg, ok := <-m.ch:
		if ok && msg != "" {
			step.Messages = append(step.Messages, types.UserMsg(msg))
		}
	default:
	}
	return step, nil
}

func (m *FollowupMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *FollowupMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
