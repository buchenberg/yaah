package pipeline

import (
	"context"
	"slices"

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

	// SteerDrain is invoked by the steer middleware after draining
	// steering messages. The composition site wires it to compaction,
	// keeping steer→compaction knowledge out of the steer middleware.
	SteerDrain DrainFunc

	ApprovalMode    string
	PermissionRules []PermissionRule

	// Approval callbacks injected by the composition site. classify
	// returns a GateDecision (pass / ask / deny / defer to global mode)
	// so origin-specific policies like mcp_approval apply even when the
	// global ApprovalMode would not gate them; approve prompts the
	// user; emitDeny fires hook events for stripped calls. When
	// classify is nil the approval middleware is inert regardless of
	// mode.
	ApprovalClassify func(name, args string) GateDecision
	ApprovalApprove  func(name, args string) bool
	ApprovalEmitDeny func(name, args, errMsg string)

	LoopDetectCount  int
	LoopDetectWindow int

	MaxToolConcurrency int

	// MaxInlineToolsPerTurn caps tool calls dispatched per turn; 0 =
	// unlimited. Enforced by the inline_limit middleware, which
	// synthesizes drop results for calls beyond the cap.
	MaxInlineToolsPerTurn int

	// ToolConc, when non-nil, is the Loop-owned semaphore instance.
	// Both orchestrator and sub-agent pipelines reuse it so there is
	// exactly one live ToolConcurrencyMiddleware per loop — the pipeline's
	// own Acquire/Release are never called by the pipeline; the Loop's
	// executeAndCollect drives the semaphore directly.
	ToolConc *ToolConcurrencyMiddleware

	MaxSubAgentConcurrency int

	// Conflict detection: tracker owned by the composition site; the
	// callbacks fire hook events, decorate the turn span, and persist
	// the injected report message. When ConflictTracker is nil the
	// middleware is inert.
	ConflictTracker *tools.ConflictTracker
	ConflictOnCheck func(ctx context.Context, model string, turn int)
	ConflictOnFound func(ctx context.Context, model string, turn int, report string, fileCount int)
	ConflictPersist func(msg types.Message)

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
	names := ResolvedPipelineNames(cfg.PipelineNames, cfg.PipelineDisabled)
	// The prompt_caching boolean knob is honored independently of the
	// name lists (review B10b): when set, the middleware is appended
	// idempotently so `prompt_caching: true` works without naming it.
	if cfg.PromptCaching && !slices.Contains(names, "prompt_caching") {
		names = append(names, "prompt_caching")
	}
	mws := make([]Middleware, 0, len(names))
	for _, name := range names {
		if build, ok := builtinBuilders[name]; ok {
			mws = append(mws, build(cfg))
		}
	}
	return NewPipeline(mws...)
}

// buildPermission is shared by the orchestrator and sub-agent pipelines.
func buildPermission(cfg PipelineConfig) Middleware {
	return &PermissionMiddleware{rules: cfg.PermissionRules}
}

// buildToolConcurrency is shared by the orchestrator and sub-agent
// pipelines. When cfg.ToolConc carries the Loop-owned instance, both
// share it instead of allocating a second semaphore.
func buildToolConcurrency(cfg PipelineConfig) Middleware {
	if cfg.ToolConc != nil {
		return cfg.ToolConc
	}
	return NewToolConcurrencyMiddleware(cfg.MaxToolConcurrency)
}

var builtinBuilders = map[string]func(PipelineConfig) Middleware{
	"steer": func(cfg PipelineConfig) Middleware {
		return &SteerMiddleware{ch: cfg.Steer, onDrain: cfg.SteerDrain}
	},
	"followup": func(cfg PipelineConfig) Middleware { return &FollowupMiddleware{ch: cfg.FollowUps} },
	"compaction": func(cfg PipelineConfig) Middleware {
		return &CompactionMiddleware{window: cfg.ContextWindow, threshold: cfg.CompactionThreshold, compactor: cfg.Compactor}
	},
	"approval": func(cfg PipelineConfig) Middleware {
		return &ApprovalMiddleware{
			mode:     cfg.ApprovalMode,
			classify: cfg.ApprovalClassify,
			approve:  cfg.ApprovalApprove,
			emitDeny: cfg.ApprovalEmitDeny,
		}
	},
	"inline_limit": func(cfg PipelineConfig) Middleware {
		return NewInlineLimitMiddleware(cfg.MaxInlineToolsPerTurn)
	},
	"loop_detection": func(cfg PipelineConfig) Middleware {
		count := cfg.LoopDetectCount
		window := cfg.LoopDetectWindow
		if count <= 0 {
			count = 5
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
	"permission":       buildPermission,
	"tool_concurrency": buildToolConcurrency,
	"prompt_caching":   func(cfg PipelineConfig) Middleware { return &PromptCachingMiddleware{enabled: cfg.PromptCaching} },
	"soft_prune": func(cfg PipelineConfig) Middleware {
		return &SoftPruneMiddleware{pruner: cfg.Pruner, emit: cfg.PruneHooks.Emit, otel: cfg.PruneHooks.Otel}
	},
	"conflict_detect": func(cfg PipelineConfig) Middleware {
		return NewConflictDetectMiddleware(cfg.ConflictTracker, cfg.ConflictOnCheck, cfg.ConflictOnFound, cfg.ConflictPersist)
	},
}

var subAgentBuilders = map[string]func(PipelineConfig) Middleware{
	"permission":       buildPermission,
	"tool_concurrency": buildToolConcurrency,
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
	// approval runs before inline_limit so denial synthesis happens on
	// the full batch and only approved calls count against the cap.
	"approval",
	"inline_limit",
	"tool_concurrency",
	"loop_detection",
	"conflict_detect",
}

// subAgentPipelineNames is the curated base middleware pipeline for
// sub-agent loops. Sub-agents are ephemeral workers with fixed turn budgets — they
// don't need orchestrator-level middleware.
//
// Deliberately excluded:
// - steer/followup: orchestrator REPL channels, sub-agents never receive them
// - approval: sub-agents auto-approve (the orchestrator gates dispatch)
// - compaction: sub-agents use CtxMgr pruning internally, not pipeline compaction
// - loop_detection: redundant with MaxLoopCycles/MaxToolTurns/WrapUpThreshold/ErrStuckChild
// - soft_prune: CtxMgr.EnsurePruner() already handles context for short-lived loops
//
// Included (conditionally, by NewSubAgentPipeline):
//   - permission: when the orchestrator passed parent permission rules they are
//     enforced inside the sub-agent loop, filtering denied tool calls before
//     concurrency gating or tracing (finding A1)
//
// Included always:
// - tool_concurrency: prevents uncontrolled parallel tool dispatch
// - shepherd_trace: records tool calls for error enrichment and supervised rollback
var subAgentPipelineNames = []string{
	"tool_concurrency",
	"shepherd_trace",
}

// DefaultPipelineNames returns a copy of the orchestrator's default
// middleware names (before disabled-list filtering). Consumers such as
// the TUI info pane use this instead of keeping their own copy of the
// list, which historically drifted out of sync.
func DefaultPipelineNames() []string {
	return slices.Clone(defaultPipelineNames)
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
//
// When cfg carries parent permission rules, the permission middleware is
// prepended so denied tool calls are stripped before any other middleware
// observes them. Opting out via PipelineDisabled is honoured.
func NewSubAgentPipeline(cfg PipelineConfig) *Pipeline {
	names := SubAgentPipelineNames(cfg.PipelineDisabled)
	if len(cfg.PermissionRules) > 0 && !slices.Contains(cfg.PipelineDisabled, "permission") {
		names = append([]string{"permission"}, names...)
	}
	mws := make([]Middleware, 0, len(names))
	for _, name := range names {
		if build, ok := subAgentBuilders[name]; ok {
			mws = append(mws, build(cfg))
		}
	}
	return NewPipeline(mws...)
}

// ResolvedPipelineNames resolves the orchestrator pipeline from the
// config name lists. middleware.enabled is ADDITIVE: it extends the
// built-in defaults rather than replacing them — replacement semantics
// silently disabled inline_limit and conflict_detect for any config
// that listed custom middleware (review B10a). middleware.disabled
// removes names from the union. Defaults keep their order; extra
// enabled names follow.
func ResolvedPipelineNames(enabled, disabled []string) []string {
	disabledSet := make(map[string]bool, len(disabled))
	for _, name := range disabled {
		disabledSet[name] = true
	}
	names := make([]string, 0, len(defaultPipelineNames)+len(enabled))
	seen := make(map[string]bool, len(defaultPipelineNames)+len(enabled))
	add := func(list []string) {
		for _, name := range list {
			if disabledSet[name] || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	add(defaultPipelineNames)
	add(enabled)
	return names
}
