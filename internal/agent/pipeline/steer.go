package pipeline

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// SteerMiddleware drains high-priority mid-turn steering messages.
type SteerMiddleware struct {
	ch        <-chan string
	compactor Compactor
}

func (m *SteerMiddleware) Name() string { return "steer" }

func (m *SteerMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	if m.ch == nil {
		return step, nil
	}
	// Drain all queued steer messages to avoid wakeup lag.
	drained := false
	for {
		select {
		case msg, ok := <-m.ch:
			if !ok {
				if drained && m.compactor != nil {
					step.Messages = m.compactor.Compact(ctx, step.Messages, 0)
				}
				return step, nil
			}
			if msg != "" {
				step.Messages = append(step.Messages, types.UserMsg("[STEER] "+msg))
				drained = true
			}
		default:
			if drained && m.compactor != nil {
				step.Messages = m.compactor.Compact(ctx, step.Messages, 0)
			}
			return step, nil
		}
	}
}

func (m *SteerMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *SteerMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
