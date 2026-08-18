package pipeline

import (
	"context"

	"github.com/buchenberg/yaah/internal/tools"
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

	// SessionID scopes Shepherd trace records. The sub-agent pipeline's
	// shepherd_trace middleware records under this owner using the
	// session-shared store (tools.SharedTraceStore).
	SessionID string

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
	"prompt_caching":   func(cfg PipelineConfig) Middleware { return &PromptCachingMiddleware{enabled: cfg.PromptCaching} },
	"soft_prune": func(cfg PipelineConfig) Middleware {
		return &SoftPruneMiddleware{pruner: cfg.Pruner, emit: cfg.PruneHooks.Emit, otel: cfg.PruneHooks.Otel}
	},
	"staleness": func(cfg PipelineConfig) Middleware { return &StalenessMiddleware{} },
}

var subAgentBuilders = map[string]func(PipelineConfig) Middleware{
	"tool_concurrency": func(cfg PipelineConfig) Middleware {
		return &ToolConcurrencyMiddleware{max: cfg.MaxToolConcurrency}
	},
	"shepherd_trace": func(cfg PipelineConfig) Middleware {
		// Sub-agents write through the session-shared store instead of
		// opening their own SQLite connection — concurrent writers on
		// the same trace.sqlite stall on busy_timeout.
		store := tools.SharedTraceStore
		if store == nil || cfg.SessionID == "" {
			return &noopShepherdTraceMiddleware{}
		}
		return &ShepherdTraceMiddleware{
			store:     store,
			sessionID: cfg.SessionID,
			ordinal:   int(nextOrdinal.Add(1 << 20)),
		}
	},
}

// noopShepherdTraceMiddleware is a placeholder returned when shepherd_trace is disabled.
type noopShepherdTraceMiddleware struct{}

func (m *noopShepherdTraceMiddleware) Name() string { return "shepherd_trace" }
func (m *noopShepherdTraceMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}
func (m *noopShepherdTraceMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}
func (m *noopShepherdTraceMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
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

// subAgentPipelineNames is the curated middleware pipeline for sub-agent
// loops. Sub-agents are ephemeral workers with fixed turn budgets — they
// don't need orchestrator-level middleware.
//
// Deliberately excluded:
// - steer/followup: orchestrator REPL channels, sub-agents never receive them
// - approval: sub-agents auto-approve (the orchestrator gates dispatch)
// - compaction: sub-agents use CtxMgr pruning internally, not pipeline compaction
// - loop_detection: redundant with MaxLoopCycles/MaxToolTurns/WrapUpThreshold/ErrStuckChild
// - staleness: orchestrator-specific (tracks steer/followup context shifts)
// - soft_prune: CtxMgr.EnsurePruner() already handles context for short-lived loops
//
// Included:
// - tool_concurrency: prevents uncontrolled parallel tool dispatch
// - shepherd_trace: records tool calls for error enrichment and supervised rollback
var subAgentPipelineNames = []string{
	"tool_concurrency",
	"shepherd_trace",
}

// SubAgentPipelineNames returns the middleware names for a sub-agent loop,
// honouring the disabled list (for opt-out of specific middleware).
func SubAgentPipelineNames(disabled []string) []string {
	disabledSet := make(map[string]bool, len(disabled))
	for _, name := range disabled {
		disabledSet[name] = true
	}
	names := make([]string, 0, len(subAgentPipelineNames))
	for _, name := range subAgentPipelineNames {
		if !disabledSet[name] {
			names = append(names, name)
		}
	}
	return names
}

// NewSubAgentPipeline builds the middleware pipeline for a sub-agent loop
// from config. It uses subAgentPipelineNames instead of the orchestrator
// defaults; the shepherd_trace builder writes through the session-shared
// store (set by InitShepherdInfrastructure during wiring).
func NewSubAgentPipeline(cfg PipelineConfig) *Pipeline {
	names := SubAgentPipelineNames(cfg.PipelineDisabled)
	mws := make([]Middleware, 0, len(names))
	for _, name := range names {
		if build, ok := subAgentBuilders[name]; ok {
			mws = append(mws, build(cfg))
		}
	}
	return NewPipeline(mws...)
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
