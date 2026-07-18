package agent

import (
	"context"
	"time"

	"github.com/buchenberg/yaah/internal/types"
)

// Step is the mutable state passed through the pipeline at each iteration.
type Step struct {
	Messages     []types.Message
	Tools        []types.ToolDef
	Iteration    int
	Model        string
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

// MiddlewareBuilder is a function that constructs a Middleware implementation
// for a given Loop. Used by the config-driven pipeline builder.
type MiddlewareBuilder func(l *Loop) Middleware

// builtinMiddleware maps canonical middleware names to their constructors.
var builtinMiddleware = map[string]MiddlewareBuilder{
	"steer": func(l *Loop) Middleware {
		return &SteerMiddleware{ch: l.Steer, compact: func(ctx context.Context) { l.compactContext(ctx, 0) }}
	},
	"followup": func(l *Loop) Middleware { return &FollowupMiddleware{ch: l.FollowUps} },
	"compaction": func(l *Loop) Middleware {
		return &CompactionMiddleware{window: l.ContextWindow, threshold: l.CompactionThreshold, loop: l}
	},
	"approval": func(l *Loop) Middleware { return &ApprovalMiddleware{mode: l.ApprovalMode} },
	"loop_detection": func(l *Loop) Middleware {
		return &LoopDetectionMiddleware{history: &l.loopHistory, count: l.LoopDetectCount, window: l.LoopDetectWindow}
	},
	"permission":       func(l *Loop) Middleware { return &PermissionMiddleware{rules: l.PermissionRules} },
	"tool_concurrency": func(l *Loop) Middleware { return &ToolConcurrencyMiddleware{max: l.MaxToolConcurrency} },
	"sub_agent":        func(l *Loop) Middleware { return &SubAgentMiddleware{MaxDepth: l.MaxSubAgentDepth} },
	"prompt_caching":   func(l *Loop) Middleware { return &PromptCachingMiddleware{enabled: l.PromptCaching} },
}

// defaultPipelineNames is the ordered list of middleware names used when no
// config overrides are provided.
var defaultPipelineNames = []string{
	"steer",
	"followup",
	"compaction",
	"approval",
	"loop_detection",
}

// resolvedPipelineNames returns the final ordered list of middleware names
// after applying config overrides (enabled/disabled lists).
func resolvedPipelineNames(enabled, disabled []string) []string {
	disabledSet := make(map[string]bool, len(disabled))
	for _, name := range disabled {
		disabledSet[name] = true
	}

	if len(enabled) > 0 {
		names := make([]string, 0, len(enabled))
		for _, name := range enabled {
			if !disabledSet[name] {
				names = append(names, name)
			}
		}
		return names
	}

	names := make([]string, 0, len(defaultPipelineNames))
	for _, name := range defaultPipelineNames {
		if !disabledSet[name] {
			names = append(names, name)
		}
	}
	return names
}
