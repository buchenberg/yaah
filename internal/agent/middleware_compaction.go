package agent

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// CompactionMiddleware triggers context compaction when token limits are approached.
type CompactionMiddleware struct {
	window int
	loop   *Loop
}

func (m *CompactionMiddleware) Name() string { return "compaction" }

func (m *CompactionMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	if m.window > 0 && m.loop != nil {
		m.loop.compactContext(ctx)
		step.Messages = m.loop.Messages
	}
	return step, nil
}

func (m *CompactionMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *CompactionMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	if m.window > 0 && m.loop != nil {
		m.loop.compactContext(ctx)
		step.Messages = m.loop.Messages
	}
	return step, nil
}
