package agent

import (
	"context"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/types"
)

// applyPruning returns the message slice to send to the provider, with
// soft-pruned tool results replaced by compact stubs. It is a no-op
// (returns the input slice unchanged) when no Pruner is attached or
// nothing has been pruned — the Pruner.Filter fast path returns the same
// backing array, so the common early-session case allocates nothing.
func (l *Loop) applyPruning(messages []types.Message) []types.Message {
	if l.Pruner == nil {
		return messages
	}
	return l.Pruner.Filter(messages)
}

// ensurePruner constructs a default Pruner when the Loop has none. Safe to
// call unconditionally: an idle Pruner (one whose Mark is never invoked
// because soft_prune is disabled) costs nothing — Filter's fast path is
// identity on an empty pruned set. This also makes disabled-mode behave
// identically to pre-change: the middleware is excluded by PipelineDisabled,
// so nothing is ever marked, so Filter stays identity.
func (l *Loop) ensurePruner() {
	if l.Pruner == nil {
		l.Pruner = pipeline.NewPruner(pipeline.DefaultPruneConfig())
	}
}

// pruneHooks wires the SoftPruneMiddleware telemetry callbacks. The JSONL
// emit hook is always set (emitHook is nil-safe via HookDir). The OTel
// hook is set only when tracing is enabled, so the middleware skips span
// creation entirely when OtelEnabled is false.
func (l *Loop) pruneHooks() pipeline.PruneHooks {
	hooks := pipeline.PruneHooks{
		Emit: l.pruneEmit,
	}
	if l.OtelEnabled {
		hooks.Otel = l.pruneOtel
	}
	return hooks
}

// pruneEmit translates a prune outcome into a best-effort JSONL hook event.
func (l *Loop) pruneEmit(s pipeline.PruneStats) {
	l.emitHook(HookEvent{
		Event:            ContextPrune,
		PruneReason:      s.Reason,
		PruneCandidates:  s.Candidates,
		PruneMarked:      s.Marked,
		PruneReclaimed:   s.ReclaimedTokens,
		PruneProtected:   s.ProtectedSkipped,
		PruneCommitted:   s.Committed,
		PruneTotalMarked: s.TotalMarked,
	})
}

// pruneOtel records a single OTel span for a prune pass. Only wired when
// OtelEnabled is true (see pruneHooks).
func (l *Loop) pruneOtel(ctx context.Context, s pipeline.PruneStats) {
	_, span := observability.StartPrune(ctx, s.Reason)
	observability.FinishPrune(span, s.Reason, s.Candidates, s.Marked, s.ReclaimedTokens, s.ProtectedSkipped, s.TotalMarked, s.Committed)
}

// resetPruner clears the soft-prune set so the fresh conversation tail is
// re-evaluated from scratch after compaction rebuilds l.Messages. Called at
// both compaction rebuild sites (summary path and trim fallback). Cumulative
// counters survive the reset for observability continuity.
func (l *Loop) resetPruner() {
	if l.Pruner != nil {
		l.Pruner.Reset()
	}
}
