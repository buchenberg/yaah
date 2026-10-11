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

// steerBuffer bounds one run's undelivered guidance. Steering is
// between-turn delivery; a healthy run drains this every step.
// SteerBuffer is the per-run steering channel capacity, exported for
// the composition sites that create the channels the registry borrows.
const SteerBuffer = 8

// Registry maps scope IDs to the steering channels of the loops
// currently running under those scopes.
type Registry struct {
	mu   sync.Mutex
	runs map[string]chan string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{runs: make(map[string]chan string)}
}

// Register associates a scope with its run's steering channel. The
// channel is owned by the run — the registry only borrows it, and
// Unregister must be called before the run closes it. A nil channel
// registers nothing: there is no loop side to deliver to, and a
// fabricated channel nobody drains would have Inject claim delivery
// into a void while status listed the scope as bridged.
func (r *Registry) Register(scopeID string, ch chan string) {
	if ch == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[scopeID] = ch
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
	ch, ok := r.runs[scopeID]
	if !ok {
		return ErrNoRun
	}
	select {
	case ch <- guidance:
		return nil
	default:
		return ErrSteerBusy
	}
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
