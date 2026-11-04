package supervision

import (
	"log/slog"
	"sync"
	"time"

	goatherd "github.com/buchenberg/goatherd-runtime"
)

// DefaultReapGrace is how long an active dispatch scope with no bridged
// run and no pending retained proposal survives before the reaper
// discards it (yaah-95ri.4). Long enough to cover dispatch-world
// provisioning and run startup for legitimately live scopes; short
// enough that orphans — forked but never finished by a crashed or
// skipped run — cannot accumulate unbounded.
const DefaultReapGrace = 10 * time.Minute

// DefaultReapInterval is the reaper's sweep cadence.
const DefaultReapInterval = 5 * time.Minute

// Reaper discards stale dispatch scopes. The fork sites and teardown
// paths own first-line cleanup; the reaper is defense in depth for
// scopes that slip past those paths (crashed runners, skipped
// settlement, recovered sessions inheriting a pre-crash scope tree).
//
// A scope is stale when it is ACTIVE, is not the root scope, has no
// bridged run in the Registry, is not reported live by the pending
// source (retained proposals awaiting settlement), and has stayed in
// that state past the grace period. Terminal scopes are not the leak —
// the manager's PruneTerminal sweeps their bookkeeping.
type Reaper struct {
	mgr         *goatherd.ScopeManager
	runs        *Registry
	rootScopeID string

	grace    time.Duration
	interval time.Duration

	mu        sync.Mutex
	pendingMu sync.RWMutex // guards pending; separate from mu — ReapOnce holds mu
	pending   func() []string
	unbridged map[string]time.Time // scope ID → first pass seen unbridged
	started   bool
	stop      chan struct{}
	stopped   chan struct{}
}

// NewReaper builds a reaper. grace <= 0 selects DefaultReapGrace.
func NewReaper(mgr *goatherd.ScopeManager, runs *Registry, rootScopeID string, grace time.Duration) *Reaper {
	if grace <= 0 {
		grace = DefaultReapGrace
	}
	return &Reaper{
		mgr:         mgr,
		runs:        runs,
		rootScopeID: rootScopeID,
		grace:       grace,
		interval:    DefaultReapInterval,
		unbridged:   make(map[string]time.Time),
		stop:        make(chan struct{}),
		stopped:     make(chan struct{}),
	}
}

// SetPendingSource registers a callback listing scope IDs that are
// alive for reasons the Registry cannot see (retained proposals
// awaiting settlement, in-flight dispatch scopes). Called on every
// sweep; safe to call concurrently with sweeps.
func (r *Reaper) SetPendingSource(src func() []string) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	r.pending = src
}

// Start runs the sweep loop in its own goroutine. Stop with Close.
func (r *Reaper) Start() {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()
	go r.loop()
}

// Close stops the sweep loop and waits for it to exit. Idempotent, and
// safe on a reaper that was never started.
func (r *Reaper) Close() {
	r.mu.Lock()
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	started := r.started
	r.mu.Unlock()
	if started {
		<-r.stopped
	}
}

func (r *Reaper) loop() {
	defer close(r.stopped)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			reaped, _, pruned := r.ReapOnce(time.Now())
			if reaped > 0 || pruned > 0 {
				slog.Info("scope reaper swept", "reaped", reaped, "pruned_terminal", pruned)
			}
		}
	}
}

// ReapOnce runs one sweep and returns (reaped, stale, pruned):
// scopes discarded this pass, active scopes seen unbridged but still
// inside the grace window, and terminal scopes pruned by the manager.
// now is injectable for tests.
func (r *Reaper) ReapOnce(now time.Time) (reaped, stale, pruned int) {
	live := r.liveSet()
	seen := make(map[string]bool, len(live))
	for _, id := range live {
		seen[id] = true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Ledger hygiene: scopes that came back live leave the ledger.
	for id := range r.unbridged {
		if seen[id] {
			delete(r.unbridged, id)
		}
	}

	if r.mgr == nil {
		return 0, 0, 0
	}
	for _, sc := range r.mgr.ActiveScopes() {
		id := sc.ID()
		if seen[id] {
			continue
		}
		first, ok := r.unbridged[id]
		if !ok {
			r.unbridged[id] = now
			stale++
			continue
		}
		if now.Sub(first) < r.grace {
			stale++
			continue
		}
		if err := r.mgr.Discard(id); err != nil {
			slog.Debug("scope reaper discard failed", "scope", id, "err", err)
			continue
		}
		delete(r.unbridged, id)
		reaped++
	}
	pruned = r.mgr.PruneTerminal()

	// Ledger hygiene: scopes discarded by their own teardown paths never
	// come back live, so their entries would linger forever. Drop any
	// ledger entry whose scope no longer exists in the manager.
	for id := range r.unbridged {
		if _, ok := r.mgr.Get(id); !ok {
			delete(r.unbridged, id)
		}
	}
	return reaped, stale, pruned
}

func (r *Reaper) liveSet() []string {
	var out []string
	if r.runs != nil {
		out = append(out, r.runs.ScopeIDs()...)
	}
	if r.rootScopeID != "" {
		out = append(out, r.rootScopeID)
	}
	r.pendingMu.RLock()
	pending := r.pending
	r.pendingMu.RUnlock()
	if pending != nil {
		out = append(out, pending()...)
	}
	return out
}
