package pipeline

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// ToolConcurrencyMiddleware limits the maximum number of concurrent tool goroutines.
type ToolConcurrencyMiddleware struct {
	max int
	sem chan struct{}
}

// NewToolConcurrencyMiddleware constructs a middleware that caps the
// number of in-flight tool goroutines at max. When max <= 0 the
// middleware is a no-op and Acquire/Release do nothing — matching the
// "0 = unlimited" convention used by Loop.MaxToolConcurrency.
func NewToolConcurrencyMiddleware(max int) *ToolConcurrencyMiddleware {
	return &ToolConcurrencyMiddleware{max: max}
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

// Acquire blocks until a tool slot is available or ctx is cancelled.
// When the middleware is configured with max <= 0 it returns immediately.
func (m *ToolConcurrencyMiddleware) Acquire(ctx context.Context) error {
	if m.max <= 0 {
		return nil
	}
	if m.sem == nil {
		m.sem = make(chan struct{}, m.max)
	}
	select {
	case m.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees a previously-acquired tool slot. Safe to call without a
// matching Acquire — it pops at most one token off the semaphore.
func (m *ToolConcurrencyMiddleware) Release() {
	if m.max <= 0 || m.sem == nil {
		return
	}
	select {
	case <-m.sem:
	default:
	}
}
