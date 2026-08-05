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
	if l.CtxMgr.Pruner == nil {
		return messages
	}
	return l.CtxMgr.Pruner.Filter(messages)
}

// pruneHooks wires the SoftPruneMiddleware telemetry callbacks. The JSONL
// emit hook is always set (emitHook is nil-safe via HookDir). The OTel
// hook is set only when tracing is enabled, so the middleware skips span
// creation entirely when OtelEnabled is false.
func (l *Loop) pruneHooks() pipeline.PruneHooks {
	hooks := pipeline.PruneHooks{
		Emit: l.pruneEmit,
	}
	if l.Config.OtelEnabled {
		hooks.Otel = l.pruneOtel
	}
	return hooks
}

// pruneEmit translates a prune outcome into a best-effort JSONL hook event.
func (l *Loop) pruneEmit(s pipeline.PruneStats) {
	l.Hooks.Emit(HookEvent{
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
