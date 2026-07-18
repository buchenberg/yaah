package agent

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// SteerMiddleware drains high-priority mid-turn steering messages.
type SteerMiddleware struct {
	ch      <-chan string
	compact func(ctx context.Context) // optional hook on injection
}

func (m *SteerMiddleware) Name() string { return "steer" }

func (m *SteerMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	if m.ch == nil {
		return step, nil
	}
	select {
	case msg, ok := <-m.ch:
		if ok && msg != "" {
			step.Messages = append(step.Messages, types.UserMsg("[STEER] "+msg))
			if m.compact != nil {
				m.compact(ctx)
			}
		}
	default:
	}
	return step, nil
}

func (m *SteerMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *SteerMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
