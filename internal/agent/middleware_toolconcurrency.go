package agent

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// ToolConcurrencyMiddleware limits the maximum number of concurrent tool goroutines.
// When max is 0 or negative, no limit is enforced (all tools run concurrently).
type ToolConcurrencyMiddleware struct {
	max int
	sem chan struct{}
}

func (m *ToolConcurrencyMiddleware) Name() string { return "tool_concurrency" }

func (m *ToolConcurrencyMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *ToolConcurrencyMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *ToolConcurrencyMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}

func (m *ToolConcurrencyMiddleware) Acquire() {
	if m.max <= 0 {
		return
	}
	if m.sem == nil {
		m.sem = make(chan struct{}, m.max)
	}
	m.sem <- struct{}{}
}

func (m *ToolConcurrencyMiddleware) Release() {
	if m.max <= 0 || m.sem == nil {
		return
	}
	select {
	case <-m.sem:
	default:
	}
}
