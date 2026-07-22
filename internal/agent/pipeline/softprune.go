package pipeline

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// PruneEmitFunc emits a hook event describing a prune outcome. Nil-safe:
// when unset, the middleware skips hook emission entirely.
type PruneEmitFunc func(PruneStats)

// PruneOtelFunc records an OpenTelemetry span for a prune outcome. Nil-safe:
// when unset, the middleware skips tracing entirely. Keeping these as
// pipeline-local function hooks (rather than importing the agent or
// observability packages) avoids an import cycle — the Loop wires its own
// emitHook and observability.StartPrune at config-build time.
type PruneOtelFunc func(ctx context.Context, stats PruneStats)

// PruneHooks carries the optional telemetry callbacks for SoftPruneMiddleware.
// Either or both may be nil; the middleware stays correct without telemetry.
type PruneHooks struct {
	Emit PruneEmitFunc
	Otel PruneOtelFunc
}

// SoftPruneMiddleware marks stale tool results after each tool batch so they
// can be elided from subsequent provider requests. It performs NO mutation of
// step.Messages: the marking is pure bookkeeping (recording tool-call IDs in
// the Pruner). The actual content stubbing happens later, at request-build
// time, when the Loop calls Pruner.Filter on a copy of the messages.
type SoftPruneMiddleware struct {
	pruner *Pruner
	emit   PruneEmitFunc
	otel   PruneOtelFunc
}

func (m *SoftPruneMiddleware) Name() string { return "soft_prune" }

func (m *SoftPruneMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *SoftPruneMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

// PostTool inspects the post-tool conversation and asks the Pruner to mark
// any stale tool results beyond the protect window. Telemetry (OTel span +
// JSONL hook) is emitted unconditionally so analysts can see "considered
// pruning, decided not to" events too.
func (m *SoftPruneMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	if m.pruner == nil {
		return step, nil
	}
	stats := m.pruner.Mark(step.Messages, "post_tool")
	if m.otel != nil {
		m.otel(ctx, stats)
	}
	if m.emit != nil {
		m.emit(stats)
	}
	return step, nil
}
