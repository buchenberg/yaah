package pipeline

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// SteerMiddleware drains high-priority mid-turn steering messages.
// It does NOT own a Compactor: when steering messages were drained, it
// invokes the OnDrain hook (wired at pipeline-assembly time to the
// compaction middleware), keeping steer→compaction knowledge out of
// this file (finding D4).
type SteerMiddleware struct {
	ch      <-chan string
	onDrain DrainFunc
}

// DrainFunc is invoked after at least one steering message was drained.
type DrainFunc func(ctx context.Context, messages []types.Message) []types.Message

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
				if drained && m.onDrain != nil {
					step.Messages = m.onDrain(ctx, step.Messages)
				}
				return step, nil
			}
			if msg != "" {
				step.Messages = append(step.Messages, types.UserMsg("[STEER] "+msg))
				drained = true
			}
		default:
			if drained && m.onDrain != nil {
				step.Messages = m.onDrain(ctx, step.Messages)
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
