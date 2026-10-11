// Package supervision bridges shepherd scopes to the loops that run
// under them (shepherd-adoption plan D2): a scope's interventions must
// reach execution, not just the trace. The Registry maps scope IDs to
// the running loops' steering channels, so the supervisor tool's inject
// (and, from step A3 on, halt) addresses something real.
//
// The registry is deliberately dumb: it knows scope IDs and channels,
// nothing about shepherded scopes or loop internals — the runner
// registers, the supervisor tool delivers, and lifecycle (a run's
// deferred unregister) keeps the map honest.
package supervision

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// ErrNoRun reports an Inject at a scope with no registered run — the
// scope may exist in the trace manager while its loop has finished (or
// never registered). Callers fall back to trace-only recording.
var ErrNoRun = errors.New("no running loop registered for this scope")

// ErrSteerBusy reports an Inject whose target loop's steering buffer is
// full: the run is not consuming guidance (wedged between turns is the
// usual cause), and blocking would stall the supervisor for a
// wedged-run timeout.
var ErrSteerBusy = errors.New("the run's steering buffer is full; it is not consuming guidance")

// ErrNotHaltable reports a Cancel at a registered run that carries no
// cancellation function — injectable but not haltable. Callers report
// the honest outcome instead of claiming a halt.
var ErrNotHaltable = errors.New("the run registered no cancellation; it is not haltable")

// steerBuffer bounds one run's undelivered guidance. Steering is
// between-turn delivery; a healthy run drains this every step.
// SteerBuffer is the per-run steering channel capacity, exported for
// the composition sites that create the channels the registry borrows.
const SteerBuffer = 8

// run is one registered execution: its steering channel (the inject
// bridge) and its cancellation function (the halt bridge, adoption plan
// A3). Either may be nil — a run registers what it supports, and Inject
// or Cancel report honestly about the rest.
type run struct {
	steerCh chan string
	cancel  context.CancelFunc
}

// Registry maps scope IDs to the loops currently running under those
// scopes.
type Registry struct {
	mu   sync.Mutex
	runs map[string]*run
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{runs: make(map[string]*run)}
}

// Register associates a scope with its run: the steering channel
// (inject) and the cancellation function (halt, adoption plan A3). The
// channel is owned by the run — the registry only borrows it, and
// Unregister must be called before the run closes it. A nil channel
// registers nothing on the inject side: there is no loop side to
// deliver to, and a fabricated channel nobody drains would have Inject
// claim delivery into a void while status listed the scope as bridged.
// A nil cancel keeps the run injectable but not haltable (Cancel then
// reports ErrNotHaltable).
func (r *Registry) Register(scopeID string, ch chan string, cancel context.CancelFunc) {
	if ch == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[scopeID] = &run{steerCh: ch, cancel: cancel}
}

// Unregister removes a run. Safe to call twice; the run's deferred
// unregister racing a session-close unregister is the normal case.
func (r *Registry) Unregister(scopeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, scopeID)
}

// Inject delivers guidance to the run's steering channel. The send
// happens under the registry lock, and Unregister must be called before
// the channel is closed, so a send can never land on a closed channel.
// Non-blocking: a full buffer means the run is not consuming guidance.
func (r *Registry) Inject(scopeID, guidance string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[scopeID]
	if !ok {
		return ErrNoRun
	}
	select {
	case run.steerCh <- guidance:
		return nil
	default:
		return ErrSteerBusy
	}
}

// Cancel halts the run registered under the scope (adoption plan A3):
// it invokes the cancellation the run registered. Idempotent per run —
// cancelling an already-cancelled run is the same call on the same
// context, a no-op by context semantics — and a scope whose run has
// finished reports ErrNoRun, never a phantom halt.
func (r *Registry) Cancel(scopeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[scopeID]
	if !ok {
		return ErrNoRun
	}
	if run.cancel == nil {
		return ErrNotHaltable
	}
	run.cancel()
	return nil
}

// Has reports whether a run is registered for the scope.
func (r *Registry) Has(scopeID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.runs[scopeID]
	return ok
}

// ScopeIDs returns the registered run scope IDs, sorted — the
// supervisor tool's status bridge report.
func (r *Registry) ScopeIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.runs))
	for id := range r.runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
