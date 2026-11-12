package supervision

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	goatherd "github.com/buchenberg/goatherd-runtime"

	"github.com/buchenberg/yaah/internal/agent/recovery"
)

// errPermanentDiscard marks a discard failure that no later sweep can turn
// into a success: the kernel's own reconstructed tree will refuse it again on
// every attempt.
//
// goatherd-runtime v0.23.0 exports no sentinel for this refusal (its
// Scope.Discard returns bare fmt.Errorf values), so classifyDiscard tags the
// refusal shapes it documents. Only the tree-shape refusals qualify, because
// nothing in the append-only trace can change them between sweeps. Everything
// else — a busy store, a deadline, an I/O fault, a scope its own teardown
// already removed, and above all the "cannot discard ..." refusal a parent gets
// while its children are still active — is transient and must stay retryable: a
// transient stranding is exactly how an orphan scope leaks for the life of the
// process. Replace the phrase list with errors.Is once upstream exports a
// sentinel.
var errPermanentDiscard = errors.New("scope reaper: the kernel permanently refuses this discard")

// permanentDiscardMessages are the ONLY refusal shapes that cannot change
// between sweeps: the reconstructed tree does not list this scope as that
// parent's child, or the scope is the root and has no parent to be discarded
// from. Both are properties of the append-only trace, so a retry would produce
// the identical refusal.
//
// The active-children refusal ("cannot discard scope: N active children") is
// deliberately NOT permanent. It reads like a tree-shape refusal, but its cause
// is a child that exists at this instant: the sweep discards deepest-first, so
// a child whose own discard failed transiently leaves its parent refused in the
// very same pass. The retry is the next sweep, after the child reaps — writing
// the parent off as permanent would strand a scope that one more sweep fixes.
// "not found" is transient for the same reason: the scope may have been
// discarded by its own teardown path, and the refused-ledger hygiene already
// drops entries for scopes the manager no longer holds.
var permanentDiscardMessages = []string{
	"is not a child of",
	"has no parent (root scope)",
}

// classifyDiscard tags a permanent refusal with errPermanentDiscard and passes
// transient failures through unchanged. It is applied at the discard seam so
// the sweep loop only has to ask errors.Is.
func classifyDiscard(err error) error {
	if err == nil || errors.Is(err, errPermanentDiscard) {
		return err
	}
	msg := err.Error()
	for _, phrase := range permanentDiscardMessages {
		if strings.Contains(msg, phrase) {
			return fmt.Errorf("%w: %w", errPermanentDiscard, err)
		}
	}
	return err
}

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
// A resumed scope that carries a sandbox is the exception (yaah-dbik):
// the substrate is live work recovery deliberately re-adopted — a
// worktree child's unmerged changes, or the root's repository — and
// Discard destroys an owned sandbox, so retiring the scope would
// physically destroy the very work recovery exists to preserve.
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
	refused   map[string]error     // scope IDs the kernel permanently refused to discard (never retried)
	transient map[string]int       // scope ID → consecutive transient discard failures (log throttling)
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
		transient:   make(map[string]int),
		stop:        make(chan struct{}),
		stopped:     make(chan struct{}),
	}
	if mgr != nil {
		// The discard seam is the manager's own discard, tagged with the
		// permanent/transient classification so the sweep loop can tell a
		// refusal (never retried) from a fault (retried next sweep). A nil
		// func keeps ReapOnce from dereferencing a reaper with no manager.
		r.discard = func(id string) error { return classifyDiscard(mgr.Discard(id)) }
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
// cap saturated by dead work. Two exemptions survive the resumed entry:
// a scope the pending source reports live (a rehydrated proposal
// awaiting settlement), and a scope that carries a re-adopted sandbox —
// live substrate a Discard would physically destroy (yaah-dbik).
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
		if resumed && sc.Sandbox() != nil {
			// A resumed scope that carries a sandbox carries a substrate
			// recovery re-adopted minutes ago (yaah-dbik): a worktree
			// child's live worktree, or the root's repository. Discard
			// destroys an owned sandbox, so retiring the scope would
			// physically destroy the work recovery just declared live —
			// the exact failure that unmade the orch-recovery scenario's
			// re-adopted worktree seconds after adoption. The skip sits
			// before the unbridged bookkeeping so no ledger entry
			// accumulates for a scope the sweep will never reap while it
			// holds the substrate. The dispatch cap does not count
			// resumed scopes (yaah-95ri.7), so sparing one holds no
			// dispatch capacity, and an explicit discard — the proposal
			// tool's — still lands; only the automatic sweep is gated.
			continue
		}
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
			if !errors.Is(err, errPermanentDiscard) {
				// A transient failure — busy store, deadline, I/O, or the
				// active-children refusal a parent gets while a sibling's
				// discard is still being retried — must not strand the scope:
				// it stays out of the refused ledger and keeps its
				// unbridged/resumed bookkeeping, so the next sweep tries
				// again.
				//
				// The retry is expected to succeed, so only the FIRST failure
				// earns a warning: a scope that keeps failing would otherwise
				// log a warning on every sweep for as long as the process
				// lives. Repeat failures drop to Debug with the attempt count,
				// and the counter is pruned with the same hygiene as the rest
				// of the ledger.
				r.transient[cand.id]++
				if attempts := r.transient[cand.id]; attempts == 1 {
					slog.Warn("scope reaper: stale scope discard failed transiently; it will be retried on the next sweep",
						"scope", cand.id, "err", err)
				} else {
					slog.Debug("scope reaper: stale scope discard failed again; still retrying",
						"scope", cand.id, "attempts", attempts, "err", err)
				}
				continue
			}
			r.refused[cand.id] = err
			delete(r.transient, cand.id)
			delete(r.unbridged, cand.id)
			delete(r.resumed, cand.id)
			slog.Warn("scope reaper: stale scope refused the discard; it will not be retried",
				"scope", cand.id, "err", err)
			continue
		}
		delete(r.transient, cand.id)
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
	// resumed, refused and transient sets get the same treatment: a resumed
	// scope its own teardown retired is gone, a refusal for a scope that no
	// longer exists is not worth remembering, and a retry counter for a
	// vanished scope must not outlive it (it would only make a recycled ID's
	// next failure log at Debug).
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
	for id := range r.transient {
		if _, ok := r.mgr.Get(id); !ok {
			delete(r.transient, id)
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
