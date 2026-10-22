package pipeline

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/buchenberg/yaah/internal/types"
)

// ConsolidateDispatch is the callback invoked by ConsolidateMiddleware when
// its cadence triggers. It is fire-and-forget: the loop continues immediately
// and the consolidation runs out-of-band. kind names the product to
// consolidate ("memory" in P0; "skills" in later phases). See
// docs/compaction-as-sleep.md §7.
type ConsolidateDispatch func(ctx context.Context, kind string)

// ConsolidateMiddleware is the cadence-driven "nudge" from
// docs/compaction-as-sleep.md §7. It counts turns and tool results and, when
// either interval is crossed, resets its counters and fires ConsolidateDispatch
// once. It never runs the consolidation inline and never mutates the
// conversation — it only enqueues.
//
// The middleware is inert (counters are not advanced) when dispatch is nil or
// both intervals are zero.
type ConsolidateMiddleware struct {
	turnsInterval int
	itersInterval int
	dispatch      ConsolidateDispatch

	turnsSince int
	itersSince int
	// inFlight is a single-flight guard: while a dispatch is running, a later
	// cadence crossing skips its fire so overlapping consolidations cannot race
	// on memory writes.
	inFlight atomic.Bool
}

// NewConsolidateMiddleware returns a consolidate middleware. Zero for either
// interval disables that dimension of the cadence; a nil dispatch makes the
// middleware inert.
func NewConsolidateMiddleware(turnsInterval, itersInterval int, dispatch ConsolidateDispatch) *ConsolidateMiddleware {
	return &ConsolidateMiddleware{
		turnsInterval: turnsInterval,
		itersInterval: itersInterval,
		dispatch:      dispatch,
	}
}

// Name returns the middleware name, used to key it in builtinBuilders and to
// resolve it from the pipeline config's enabled/disabled lists.
func (m *ConsolidateMiddleware) Name() string { return "consolidate" }

// PrepareStep is a no-op: the nudge is cadence-driven and does not touch the
// request under construction.
func (m *ConsolidateMiddleware) PrepareStep(_ context.Context, step *Step) (*Step, error) {
	return step, nil
}

// PostModel is a no-op: the nudge fires on tool results, not on model output.
func (m *ConsolidateMiddleware) PostModel(_ context.Context, _ *types.Message, step *Step) (*Step, error) {
	return step, nil
}

// PostTool advances the cadence counters and, if an interval is crossed, fires
// the consolidation dispatch once and resets both counters. The dispatch is
// fire-and-forget: it runs on a detached goroutine with a context that is not
// cancelled when the turn or run ends, so it can neither block the loop nor be
// killed mid-flight.
func (m *ConsolidateMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	if m.dispatch == nil {
		return step, nil
	}

	m.turnsSince++
	m.itersSince += len(results)

	var fire bool
	switch {
	case m.turnsInterval > 0 && m.turnsSince >= m.turnsInterval:
		fire = true
	case m.itersInterval > 0 && m.itersSince >= m.itersInterval:
		fire = true
	}

	if !fire {
		return step, nil
	}

	m.reset()

	// Single-flight: skip if a previous dispatch is still in flight.
	if !m.inFlight.CompareAndSwap(false, true) {
		return step, nil
	}

	dispatch := m.dispatch
	go func() {
		defer m.inFlight.Store(false)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("consolidate: dispatch panicked", "kind", "memory", "panic", r)
			}
		}()
		dispatch(context.WithoutCancel(ctx), "memory")
	}()

	return step, nil
}

func (m *ConsolidateMiddleware) reset() {
	m.turnsSince = 0
	m.itersSince = 0
}
