package pipeline

import (
	"context"
	"slices"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/config"
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

	// Approval callbacks injected by the composition site. authorize
	// returns the one authorization decision for a call (the same value
	// the always-on dispatch gate consumes in package agent), so every
	// policy — the global `approval` posture, mcp_approval, the mode floor,
	// and the user ruleset — is already resolved into the gate it reports;
	// approve prompts the user; emitDeny fires hook events for stripped
	// calls. When authorize is nil the approval middleware is inert.
	ApprovalAuthorize func(name, args string) Decision
	ApprovalApprove   func(ctx context.Context, callID, name, args string) bool
	ApprovalEmitDeny  func(name, args, errMsg string)

	LoopDetectCount  int
	LoopDetectWindow int

	// IntentSteerAfter / IntentHaltAfter configure the cross-turn
	// intent-repetition detector: steer after N consecutive same-plan
	// turns, halt after N. Zero uses the defaults (3 / 8).
	IntentSteerAfter int
	IntentHaltAfter  int

	// EvidenceGuardNudge / EvidenceGuardHalt configure the stale-evidence
	// guard. Nudge 0 = default (6); negative = disabled; halt 0 = disabled.
	EvidenceGuardNudge int
	EvidenceGuardHalt  int

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
	// session-shared store (TraceStore, injected by the wiring — plan
	// 07, H4), and the supervisor_guard middleware addresses its
	// checks to the same owner.
	SessionID string

	// TraceStore is the session-wide Shepherd trace sink (plan 07,
	// H4): session-owned and injected by the wiring, like the
	// supervisor registry — sub-agent middleware writes through this
	// shared connection instead of reaching through package globals.
	// Nil when tracing is disabled. The seam, not the kernel's concrete
	// store: pipeline code only ever appends facts and publishes
	// frontiers through it (see TraceSink in scope_init.go).
	TraceStore TraceSink

	// RetainedContext, when set, is stamped onto every fact group this
	// middleware appends. Sub-agents use it to bind their transcript to the
	// dispatch's substrate, containment, and role tool belt without relying
	// on mutable session columns (yaah-o86).
	RetainedContext *shepherdtrace.RetainedContext

	// DestructiveGuard is the supervision service's synchronous
	// pre-dispatch check (adoption plan B2): a non-empty return denies
	// the tool call before execution. Nil means the guard is disabled —
	// every call passes, so tracing-off sessions are unchanged.
	DestructiveGuard func(ownerID, toolName, argsJSON string) string

	// ConsolidateDispatch is the cadence-driven "nudge" callback
	// (docs/compaction-as-sleep.md §7). When the consolidate middleware's
	// turn or tool-result interval is crossed it fires fire-and-forget and
	// resets its counters. kind names the product to consolidate ("memory"
	// in P0; "skills" later). Nil disables the nudge.
	ConsolidateDispatch ConsolidateDispatch

	// ConsolidateTurnsInterval and ConsolidateItersInterval set the nudge
	// cadence: fire every N turns or every N tool results. Zero disables
	// that dimension; both zero with a nil dispatch makes the middleware
	// inert.
	ConsolidateTurnsInterval int
	ConsolidateItersInterval int
}

// NewFromConfig builds the orchestrator pipeline. The set is fixed: every
// built-in middleware runs, because each one is either a safety net
// (compaction, evidence/loop/intent guards, the dispatch-adjacent approval
// pipeline) or required plumbing (steer/followup channels, tracing). The
// only optional middleware is prompt_caching, controlled by its own
// boolean, and consolidate, which is inert unless its cadence is set.
func NewFromConfig(cfg PipelineConfig) *Pipeline {
	names := ActiveOrchestratorNames(cfg.PromptCaching)
	mws := make([]Middleware, 0, len(names))
	for _, name := range names {
		if build, ok := builtinBuilders[name]; ok {
			mws = append(mws, build(cfg))
		}
	}
	return NewPipeline(mws...)
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

// buildShepherdTrace is shared by the orchestrator and sub-agent pipelines.
// Both write through the session-shared store; nil store/session falls back to
// the noop slot so the pipeline shape stays stable.
func buildShepherdTrace(cfg PipelineConfig) Middleware {
	store := cfg.TraceStore
	// A tracing-off session carries a nil kernel store in a non-nil interface
	// (the composition root still holds the concrete handle), so the nil test
	// goes through the seam's own predicate: a typed-nil sink must resolve to
	// the noop slot, exactly as it did when the field held the bare pointer.
	if SinkIsNil(store) || cfg.SessionID == "" {
		return &noopShepherdTraceMiddleware{}
	}
	return &ShepherdTraceMiddleware{
		store:           store,
		sessionID:       cfg.SessionID,
		ordinal:         int(nextOrdinal.Add(1 << 20)),
		retainedContext: cfg.RetainedContext,
	}
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
			authorize: cfg.ApprovalAuthorize,
			approve:   cfg.ApprovalApprove,
			emitDeny:  cfg.ApprovalEmitDeny,
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
	"intent_loop": func(cfg PipelineConfig) Middleware {
		steer, halt := cfg.IntentSteerAfter, cfg.IntentHaltAfter
		if steer <= 0 {
			steer = config.DefaultIntentSteerAfter
		}
		if halt <= 0 {
			halt = config.DefaultIntentHaltAfter
		}
		return NewIntentLoopMiddleware(steer, halt)
	},
	"evidence_guard": func(cfg PipelineConfig) Middleware {
		return NewEvidenceGuardMiddleware(cfg.EvidenceGuardNudge, cfg.EvidenceGuardHalt)
	},
	"tool_concurrency": buildToolConcurrency,
	"shepherd_trace":   buildShepherdTrace,
	"prompt_caching":   func(cfg PipelineConfig) Middleware { return &PromptCachingMiddleware{enabled: cfg.PromptCaching} },
	"soft_prune": func(cfg PipelineConfig) Middleware {
		return &SoftPruneMiddleware{pruner: cfg.Pruner, emit: cfg.PruneHooks.Emit, otel: cfg.PruneHooks.Otel}
	},
	"conflict_detect": func(cfg PipelineConfig) Middleware {
		return NewConflictDetectMiddleware(cfg.ConflictTracker, cfg.ConflictOnCheck, cfg.ConflictOnFound, cfg.ConflictPersist)
	},
	// consolidate: the cadence-driven "nudge" that fires ConsolidateDispatch
	// out-of-band (docs/compaction-as-sleep.md §7). Inert when dispatch is
	// nil or both intervals are zero.
	"consolidate": func(cfg PipelineConfig) Middleware {
		return NewConsolidateMiddleware(cfg.ConsolidateTurnsInterval, cfg.ConsolidateItersInterval, cfg.ConsolidateDispatch)
	},
}

var subAgentBuilders = map[string]func(PipelineConfig) Middleware{
	// supervisor_guard: the blocking pre-dispatch net (adoption plan
	// B2). Installed only when cfg.DestructiveGuard is wired; the
	// builder below would otherwise install a pass-through no-op.
	"supervisor_guard": func(cfg PipelineConfig) Middleware {
		// The middleware no-ops on a nil check, so a stale config name
		// can never deny anything; NewSubAgentPipeline installs it
		// only when cfg.DestructiveGuard is wired.
		return NewSupervisorGuardMiddleware(cfg.DestructiveGuard, cfg.SessionID)
	},
	// steer: the supervision inject channel (adoption plan A2). Unlike
	// the orchestrator builder, no onDrain compaction hook — sub-agent
	// loops prune via CtxMgr, and the builder is only installed when
	// cfg.Steer is wired, so nil channels never reach the middleware.
	"steer": func(cfg PipelineConfig) Middleware {
		return &SteerMiddleware{ch: cfg.Steer}
	},
	"tool_concurrency": buildToolConcurrency,
	"shepherd_trace":   buildShepherdTrace,
	"evidence_guard": func(cfg PipelineConfig) Middleware {
		return NewEvidenceGuardMiddleware(cfg.EvidenceGuardNudge, cfg.EvidenceGuardHalt)
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
	"shepherd_trace",
	"loop_detection",
	"intent_loop",
	"evidence_guard",
	"conflict_detect",
	// consolidate is the cadence-driven memory nudge. It is inert unless
	// a dispatch is wired and one of the consolidate intervals is set, so
	// it can live in the default set and be driven entirely by its own
	// knobs (docs/compaction-as-sleep.md §7).
	"consolidate",
}

// subAgentPipelineNames is the curated base middleware pipeline for
// sub-agent loops. Sub-agents are ephemeral workers with fixed turn budgets — they
// don't need orchestrator-level middleware.
//
// Deliberately excluded:
//   - followup: orchestrator REPL channel, sub-agents never receive it
//   - approval: a sub-agent has no approval bridge (no user is attached to
//     the dispatch), so the middleware could only ever ask an unanswerable
//     question. The dispatch gate (agent.Loop.admitTool) therefore refuses
//     every terminal verdict outright — a deny from any posture or rule, and
//     an ask — instead of letting an ask through as an allow.
//   - compaction: sub-agents use CtxMgr pruning internally, not pipeline compaction
//   - loop_detection: redundant with MaxLoopCycles/MaxToolTurns/WrapUpThreshold/ErrStuckChild
//   - soft_prune: CtxMgr.EnsurePruner() already handles context for short-lived loops
//
// Included (conditionally, by NewSubAgentPipeline):
//   - steer: when the run carries a supervision inject channel
//     (shepherd-adoption plan A2) — the supervisor's guidance must reach
//     the loop between turns. Without a channel there is nothing to
//     drain, and the orchestrator's REPL steering remains excluded.
//
// Included always:
// - tool_concurrency: prevents uncontrolled parallel tool dispatch
// - shepherd_trace: records tool calls for error enrichment and sub-agent diagnostics
var subAgentPipelineNames = []string{
	"tool_concurrency",
	"shepherd_trace",
	"evidence_guard",
}

// DefaultPipelineNames returns a copy of the orchestrator's fixed
// middleware set. Consumers such as the doctor and the docs inventory
// check use this instead of keeping their own copy of the list.
func DefaultPipelineNames() []string {
	return slices.Clone(defaultPipelineNames)
}

// SubAgentPipelineNames returns the fixed middleware base for a sub-agent
// loop. Callers may prepend conditional entries (steer, supervisor_guard)
// when the corresponding dependency is wired — see NewSubAgentPipeline.
func SubAgentPipelineNames() []string {
	return slices.Clone(subAgentPipelineNames)
}

// NewSubAgentPipeline builds the middleware pipeline for a sub-agent loop
// from config. It uses subAgentPipelineNames instead of the orchestrator
// defaults; the shepherd_trace builder writes through the session-shared
// store (set by InitShepherdInfrastructure during wiring).
//
// The sub-agent pipeline is built from subAgentPipelineNames; conditional
// prepends (steer, supervisor_guard) are documented on that list.
func NewSubAgentPipeline(cfg PipelineConfig) *Pipeline {
	names := SubAgentPipelineNames()
	// Prepend in reverse of the intended order so the last prepend is
	// first: steer must run before everything — injected guidance has to
	// reach the model before it repeats the mistake — and the supervisor
	// guard second, the blocking net before anything dispatches.
	if cfg.DestructiveGuard != nil {
		names = append([]string{"supervisor_guard"}, names...)
	}
	if cfg.Steer != nil {
		names = append([]string{"steer"}, names...)
	}
	mws := make([]Middleware, 0, len(names))
	for _, name := range names {
		if build, ok := subAgentBuilders[name]; ok {
			mws = append(mws, build(cfg))
		}
	}
	return NewPipeline(mws...)
}

// ActiveOrchestratorNames returns the names NewFromConfig would install for
// this config — the fixed default set plus prompt_caching when its own
// boolean knob is set. The doctor and docs consumers use this instead of
// re-deriving the composition.
func ActiveOrchestratorNames(promptCaching bool) []string {
	names := DefaultPipelineNames()
	if promptCaching && !slices.Contains(names, "prompt_caching") {
		names = append(names, "prompt_caching")
	}
	return names
}
