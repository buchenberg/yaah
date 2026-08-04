package pipeline

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// Compactor summarizes or trims conversation messages when they exceed
// token thresholds. It takes the current messages, may compact old ones,
// and returns the result.
type Compactor interface {
	Compact(ctx context.Context, messages []types.Message, threshold float64) []types.Message
}

// PipelineConfig holds all configuration needed to build the default pipeline.
type PipelineConfig struct {
	Steer     <-chan string
	FollowUps <-chan string

	ContextWindow       int
	CompactionThreshold float64
	Compactor           Compactor

	ApprovalMode    string
	PermissionRules []PermissionRule

	LoopDetectCount  int
	LoopDetectWindow int

	MaxToolConcurrency int

	MaxSubAgentConcurrency int

	PromptCaching bool

	// Pruner soft-prunes stale tool-result content from provider requests.
	// When non-nil and soft_prune is in the pipeline, the SoftPruneMiddleware
	// marks stale results after each tool batch; the Loop stubs them at
	// request-build time. PruneHooks wires optional telemetry (nil-safe).
	Pruner     *Pruner
	PruneHooks PruneHooks

	PipelineNames    []string
	PipelineDisabled []string
}

// NewFromConfig builds the default pipeline from config, honouring
// enabled/disabled name lists.
func NewFromConfig(cfg PipelineConfig) *Pipeline {
	names := resolvedPipelineNames(cfg.PipelineNames, cfg.PipelineDisabled)
	mws := make([]Middleware, 0, len(names))
	for _, name := range names {
		if build, ok := builtinBuilders[name]; ok {
			mws = append(mws, build(cfg))
		}
	}
	return NewPipeline(mws...)
}

var builtinBuilders = map[string]func(PipelineConfig) Middleware{
	"steer": func(cfg PipelineConfig) Middleware {
		return &SteerMiddleware{ch: cfg.Steer, compactor: cfg.Compactor}
	},
	"followup": func(cfg PipelineConfig) Middleware { return &FollowupMiddleware{ch: cfg.FollowUps} },
	"compaction": func(cfg PipelineConfig) Middleware {
		return &CompactionMiddleware{window: cfg.ContextWindow, threshold: cfg.CompactionThreshold, compactor: cfg.Compactor}
	},
	"approval": func(cfg PipelineConfig) Middleware { return &ApprovalMiddleware{mode: cfg.ApprovalMode} },
	"loop_detection": func(cfg PipelineConfig) Middleware {
		count := cfg.LoopDetectCount
		window := cfg.LoopDetectWindow
		if count <= 0 {
			count = 4
		}
		if window <= 0 {
			window = 10
		}
		// window must hold at least count items, otherwise detection is
		// trivially triggered or impossible (e.g. window=2, count=3).
		if window < count {
			window = count
		}
		return &LoopDetectionMiddleware{count: count, window: window}
	},
	"permission":       func(cfg PipelineConfig) Middleware { return &PermissionMiddleware{rules: cfg.PermissionRules} },
	"tool_concurrency": func(cfg PipelineConfig) Middleware { return &ToolConcurrencyMiddleware{max: cfg.MaxToolConcurrency} },
	"prompt_caching": func(cfg PipelineConfig) Middleware { return &PromptCachingMiddleware{enabled: cfg.PromptCaching} },
	"soft_prune": func(cfg PipelineConfig) Middleware {
		return &SoftPruneMiddleware{pruner: cfg.Pruner, emit: cfg.PruneHooks.Emit, otel: cfg.PruneHooks.Otel}
	},
	"staleness": func(cfg PipelineConfig) Middleware { return &StalenessMiddleware{} },
}

var defaultPipelineNames = []string{
	"steer",
	"followup",
	"compaction",
	"soft_prune",
	"approval",
	"tool_concurrency",
	"loop_detection",
	"staleness",
}

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
