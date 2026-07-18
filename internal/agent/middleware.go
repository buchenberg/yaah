package agent

import (
	"context"
	"time"

	"github.com/buchenberg/yaah/internal/types"
)

// Step is the mutable state passed through the pipeline at each iteration.
type Step struct {
	Messages   []types.Message
	Tools      []types.ToolDef
	Iteration  int
	Model      string
	SystemPrompt string
}

// ToolResult holds the outcome of a single tool execution for middleware inspection.
type ToolResult struct {
	Name     string
	Args     string
	Result   string
	Error    error
	Duration time.Duration
}

// Middleware intercepts the agent loop at well-defined points.
type Middleware interface {
	Name() string

	// PrepareStep is called before each model call.
	PrepareStep(ctx context.Context, step *Step) (*Step, error)

	// PostModel is called after the model responds, before tool execution.
	PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error)

	// PostTool is called after all tools in this iteration have executed.
	PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error)
}
