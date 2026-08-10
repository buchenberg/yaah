package pipeline

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// Pipeline executes a sequence of Middleware hooks.
type Pipeline struct {
	middleware []Middleware
}

// NewPipeline returns a Pipeline wrapping the given middleware.
func NewPipeline(middleware ...Middleware) *Pipeline {
	return &Pipeline{middleware: middleware}
}

// RunPrepareStep calls PrepareStep on every middleware in order.
func (p *Pipeline) RunPrepareStep(ctx context.Context, step *Step) (*Step, error) {
	var err error
	for _, mw := range p.middleware {
		step, err = mw.PrepareStep(ctx, step)
		if err != nil {
			return step, fmt.Errorf("%s: %w", mw.Name(), err)
		}
	}
	return step, nil
}

// RunPostModel calls PostModel on every middleware in order.
func (p *Pipeline) RunPostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	var err error
	for _, mw := range p.middleware {
		step, err = mw.PostModel(ctx, msg, step)
		if err != nil {
			return step, fmt.Errorf("%s: %w", mw.Name(), err)
		}
	}
	return step, nil
}

// RunPostTool calls PostTool on every middleware in order.
func (p *Pipeline) RunPostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	var err error
	for _, mw := range p.middleware {
		step, err = mw.PostTool(ctx, results, step)
		if err != nil {
			return step, fmt.Errorf("%s: %w", mw.Name(), err)
		}
	}
	return step, nil
}

// MiddlewareNames returns the names of the middleware in this pipeline.
func (p *Pipeline) MiddlewareNames() []string {
	names := make([]string, len(p.middleware))
	for i, mw := range p.middleware {
		names[i] = mw.Name()
	}
	return names
}

// Find returns the first middleware with the given name, or nil if the
// pipeline doesn't contain one. Callers use this to locate middleware
// that expose methods beyond the standard hook interface (e.g. the
// tool_concurrency middleware's Acquire/Release used to gate per-tool
// goroutines).
func (p *Pipeline) Find(name string) Middleware {
	for _, mw := range p.middleware {
		if mw.Name() == name {
			return mw
		}
	}
	return nil
}

// ShepherdTraceMiddleware returns the ShepherdTraceMiddleware in this
// pipeline, or nil if tracing is not configured.
func (p *Pipeline) ShepherdTraceMiddleware() *ShepherdTraceMiddleware {
	mw := p.Find("shepherd_trace")
	if t, ok := mw.(*ShepherdTraceMiddleware); ok {
		return t
	}
	return nil
}
