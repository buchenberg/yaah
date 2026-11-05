package supervision

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	goatherd "github.com/buchenberg/goatherd-runtime"

	"github.com/buchenberg/yaah/internal/agent/recovery"
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
//
// Resumed scopes — the ones startup recovery re-admitted from the trace
// (yaah-95ri.7) — are stale from the first pass: no run of this process
// ever bridged them, and their unbridged clock started before the
// process did, so the grace window that protects a freshly forked scope
// would only delay the sweep while dead work holds the dispatch cap.
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
	resumed   map[string]bool      // scope IDs startup recovery re-admitted from the trace
	refused   map[string]error     // scope IDs the kernel refused to discard (never retried)
	discard   func(id string) error
	started   bool
	stop      chan struct{}
	stopped   chan struct{}
}

// NewReaper builds a reaper. grace <= 0 selects DefaultReapGrace.
func NewReaper(mgr *goatherd.ScopeManager, runs *Registry, rootScopeID string, grace time.Duration) *Reaper {
	if grace <= 0 {
		grace = DefaultReapGrace
	}
	r := &Reaper{
		mgr:         mgr,
		runs:        runs,
		rootScopeID: rootScopeID,
		grace:       grace,
		interval:    DefaultReapInterval,
		unbridged:   make(map[string]time.Time),
		resumed:     make(map[string]bool),
		refused:     make(map[string]error),
		stop:        make(chan struct{}),
		stopped:     make(chan struct{}),
	}
	if mgr != nil {
		// The discard seam is the manager's own discard: a reaper without
		// a manager has nothing to sweep, and a nil func keeps ReapOnce
		// from dereferencing it.
		r.discard = mgr.Discard
	}
	return r
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

// SetResumed registers the scope IDs startup recovery re-admitted from
// the trace (yaah-95ri.7). Call it before Start: the wiring has the set
// once, when recovery rebuilds the tree. A resumed scope is past grace
// on its first sweep — nothing of this process ever bridged it, and its
// unbridged clock began before the process did — so the reaper retires
// it instead of granting a grace window that would leave the dispatch
// cap saturated by dead work. Scopes the pending source reports live
// (a rehydrated proposal awaiting settlement) are still spared.
func (r *Reaper) SetResumed(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resumed == nil {
		r.resumed = make(map[string]bool, len(ids))
	}
	for _, id := range ids {
		if id != "" {
			r.resumed[id] = true
		}
	}
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
	// One sweep before the ticker (yaah-95ri.7): the scopes that can be
	// stale this early are the resumed ones, and making them wait out a
	// whole interval would keep the dispatch cap saturated by dead work
	// for the session's opening minutes. Nothing has been forked yet, so
	// no live dispatch is at risk.
	r.reapAndLog(time.Now())
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
			r.reapAndLog(time.Now())
		}
	}
}

// reapAndLog runs one sweep and reports a non-quiet result.
func (r *Reaper) reapAndLog(now time.Time) {
	reaped, _, pruned := r.ReapOnce(now)
	if reaped > 0 || pruned > 0 {
		slog.Info("scope reaper swept", "reaped", reaped, "pruned_terminal", pruned)
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

	if r.mgr == nil || r.discard == nil {
		return 0, 0, 0
	}

	// Decide who is stale first, then discard deepest-first. A child must
	// go before its parent: the kernel rebuilds the tree from the trace,
	// and discarding a scope that still owns active children is refused —
	// which would leave half a dead tree standing and the parent
	// permanently unresolvable (yaah-95ri.7).
	type staleScope struct {
		id      string
		resumed bool
		depth   int
	}
	var ready []staleScope
	for _, sc := range r.mgr.ActiveScopes() {
		id := sc.ID()
		if seen[id] {
			continue
		}
		if _, ok := r.refused[id]; ok {
			// A scope the kernel refused to discard (yaah-95ri.7) is
			// recorded once and never retried: the refusal comes from the
			// trace's own reconstructed tree, so a retry cannot succeed,
			// and a sweep that keeps trying turns one unreapable scope
			// into a log storm beside every dispatch. The scope keeps its
			// trace record — nothing here fabricates a terminal state —
			// and it stays out of the dispatch cap by the same
			// resumed-scope exclusion that brought it here.
			continue
		}
		resumed := r.resumed[id]
		first, ok := r.unbridged[id]
		if !ok {
			if !resumed {
				r.unbridged[id] = now
				stale++
				continue
			}
			first = now // resumed: past grace from the first pass
		}
		if !resumed && now.Sub(first) < r.grace {
			stale++
			continue
		}
		ready = append(ready, staleScope{id: id, resumed: resumed, depth: scopeDepth(sc)})
	}
	sort.Slice(ready, func(i, j int) bool {
		if ready[i].depth != ready[j].depth {
			return ready[i].depth > ready[j].depth
		}
		return ready[i].id < ready[j].id
	})
	for _, cand := range ready {
		if err := r.discard(cand.id); err != nil {
			r.refused[cand.id] = err
			delete(r.unbridged, cand.id)
			delete(r.resumed, cand.id)
			slog.Warn("scope reaper: stale scope refused the discard; it will not be retried",
				"scope", cand.id, "err", err)
			continue
		}
		delete(r.unbridged, cand.id)
		delete(r.resumed, cand.id)
		reaped++
		if cand.resumed {
			slog.Info("scope reaper: resumed scope retired",
				"scope", cand.id, "execution", recovery.ResumedExecution)
		}
	}
	pruned = r.mgr.PruneTerminal()

	// Ledger hygiene: scopes discarded by their own teardown paths never
	// come back live, so their entries would linger forever. Drop any
	// ledger entry whose scope no longer exists in the manager. The
	// resumed and refused sets get the same treatment: a resumed scope its
	// own teardown retired is gone, and a refusal for a scope that no
	// longer exists is not worth remembering.
	for id := range r.unbridged {
		if _, ok := r.mgr.Get(id); !ok {
			delete(r.unbridged, id)
		}
	}
	for id := range r.resumed {
		if _, ok := r.mgr.Get(id); !ok {
			delete(r.resumed, id)
		}
	}
	for id := range r.refused {
		if _, ok := r.mgr.Get(id); !ok {
			delete(r.refused, id)
		}
	}
	return reaped, stale, pruned
}

// maxScopeDepth bounds the parent walk in scopeDepth.
const maxScopeDepth = 64

// scopeDepth counts a scope's ancestors: a root is depth 0. The walk is
// bounded so a reconstructed parent chain that loops (a corrupt trace)
// cannot hang a sweep.
func scopeDepth(scope *goatherd.Scope) int {
	depth := 0
	for cur := scope; cur != nil; {
		if depth >= maxScopeDepth {
			return depth
		}
		cur = cur.Parent()
		if cur == nil {
			return depth
		}
		depth++
	}
	return depth
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
