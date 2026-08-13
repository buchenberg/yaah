// context.go defines the context-key contract between the task tool,
// the sub-agent runner, and the agent loop: model writeback, start
// notification, usage accumulation, and stuck-child heartbeats. These
// keys are set by the spawner and read/written by the runner and loop.
package jobs

import (
	"context"
	"errors"

	"github.com/buchenberg/yaah/internal/types"
)

// contextKey is the package-local context key type for string keys.
type contextKey string

// subAgentModelKey stores the sub-agent's model name in the context so
// the caller can read it after execution.
const subAgentModelKey contextKey = "yaah-subagent-model"

// SubAgentModelFromContext returns the model used by a completed sub-agent.
func SubAgentModelFromContext(ctx context.Context) string {
	if v := ctx.Value(subAgentModelKey); v != nil {
		return v.(string)
	}
	return ""
}

// subAgentModelPtrKey is a context key for a *string the runner writes
// the sub-agent's model into.
type subAgentModelPtrKey struct{}

// WithSubAgentModelPtr stores ptr in ctx so the sub-agent runner can write
// the model name into it. Call after runner returns to read the value.
func WithSubAgentModelPtr(ctx context.Context, ptr *string) context.Context {
	return context.WithValue(ctx, subAgentModelPtrKey{}, ptr)
}

// WriteSubAgentModel writes model to the *string stored in ctx, if present.
func WriteSubAgentModel(ctx context.Context, model string) {
	if ptr, ok := ctx.Value(subAgentModelPtrKey{}).(*string); ok {
		*ptr = model
	}
}

// subAgentStartKey is a context key for the start notifier the runner
// fires once the sub-agent's provider and model are resolved, just
// before its loop begins.
type subAgentStartKey struct{}

// WithSubAgentStartNotifier stores fn in ctx so the sub-agent runner can
// announce the resolved model at start time. The caller uses this to
// emit a start event that already knows which model the child runs on.
func WithSubAgentStartNotifier(ctx context.Context, fn func(model string)) context.Context {
	return context.WithValue(ctx, subAgentStartKey{}, fn)
}

// NotifySubAgentStart invokes the start notifier stored in ctx, if
// present. Call from the runner after the sub-agent model is resolved.
func NotifySubAgentStart(ctx context.Context, model string) {
	if fn, ok := ctx.Value(subAgentStartKey{}).(func(string)); ok {
		fn(model)
	}
}

// subAgentUsageKey stores a *types.Usage pointer for accumulating
// sub-agent token usage in the caller.
type subAgentUsageKey struct{}

// WithSubAgentUsage sets a usage accumulator in ctx so the caller can
// collect sub-agent token counts.
func WithSubAgentUsage(ctx context.Context, usage *types.Usage) context.Context {
	return context.WithValue(ctx, subAgentUsageKey{}, usage)
}

// AddSubAgentUsage adds delta to the usage accumulator in ctx, if present.
func AddSubAgentUsage(ctx context.Context, delta types.Usage) {
	if acc, ok := ctx.Value(subAgentUsageKey{}).(*types.Usage); ok {
		acc.PromptTokens += delta.PromptTokens
		acc.CompletionTokens += delta.CompletionTokens
		acc.TotalTokens += delta.TotalTokens
	}
}

// TurnRestoreStats reports turn-level checkpoint restores performed by a
// sub-agent loop. The spawner stores a pointer in the context
// (WithTurnRestoreStats); the loop records each restore into it
// (RecordTurnRestore); the spawner reads it after the run to enrich its
// result envelope.
type TurnRestoreStats struct {
	Restores     int
	RestoredFrom string
}

type turnRestoreStatsKey struct{}

// WithTurnRestoreStats stores stats in ctx so the sub-agent loop can
// record turn restores into it. Read stats after the runner returns.
func WithTurnRestoreStats(ctx context.Context, stats *TurnRestoreStats) context.Context {
	return context.WithValue(ctx, turnRestoreStatsKey{}, stats)
}

// RecordTurnRestore increments the restore counter stored in ctx, if
// present, and records the checkpoint ID restored from.
func RecordTurnRestore(ctx context.Context, restoredFrom string) {
	if s, ok := ctx.Value(turnRestoreStatsKey{}).(*TurnRestoreStats); ok {
		s.Restores++
		s.RestoredFrom = restoredFrom
	}
}

// subAgentHeartbeatKey is a context key for the per-sub-agent heartbeat
// channel. The sub-agent loop non-blocking-sends on this channel each
// iteration so a parent watchdog can detect stuck children.
type subAgentHeartbeatKey struct{}

// WithSubAgentHeartbeat stores hb in ctx so the sub-agent loop can emit
// heartbeats. The caller should create a buffered channel (cap 1) so the
// sub-agent never blocks on send.
func WithSubAgentHeartbeat(ctx context.Context, hb chan struct{}) context.Context {
	return context.WithValue(ctx, subAgentHeartbeatKey{}, hb)
}

// SendHeartbeat non-blocking-sends on the heartbeat channel stored in ctx,
// if present. Designed to be called at the top of each agent loop iteration.
func SendHeartbeat(ctx context.Context) {
	if hb, ok := ctx.Value(subAgentHeartbeatKey{}).(chan struct{}); ok {
		select {
		case hb <- struct{}{}:
		default:
		}
	}
}

// ErrStuckChild is returned when a sub-agent is cancelled by the parent
// watchdog after StuckChildTimeout elapses with no heartbeat.
var ErrStuckChild = errors.New("sub-agent stuck: no heartbeat received within deadline")
