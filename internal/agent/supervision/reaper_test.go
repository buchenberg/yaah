package supervision

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/goatherd-runtime"
)

// newReaperFixture builds a real ScopeManager over a temp SQLite trace
// store with a root scope, and a reaper with a 2-second grace period so
// tests can cross the grace line with injected times.
func newReaperFixture(t *testing.T) (*Reaper, *goatherd.ScopeManager, *Registry, string) {
	t.Helper()
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	mgr := goatherd.NewScopeManager(store)
	root, err := mgr.Create("reaper-test-root", nil)
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	runs := NewRegistry()
	r := NewReaper(mgr, runs, root.ID(), 2*time.Second)
	t.Cleanup(r.Close)
	return r, mgr, runs, root.ID()
}

func forkChild(t *testing.T, mgr *goatherd.ScopeManager, rootID, id string) {
	t.Helper()
	if _, err := mgr.Fork(rootID, id, nil); err != nil {
		t.Fatalf("fork %s: %v", id, err)
	}
}

func TestReapOnceDiscardsOrphanAfterGrace(t *testing.T) {
	r, mgr, _, rootID := newReaperFixture(t)
	forkChild(t, mgr, rootID, "orphan")

	now := time.Now()

	// First pass: the orphan is unbridged but fresh — recorded, not reaped.
	reaped, stale, _ := r.ReapOnce(now)
	if reaped != 0 || stale != 1 {
		t.Fatalf("first pass: reaped=%d stale=%d, want 0/1", reaped, stale)
	}
	if _, ok := mgr.Get("scope:orphan"); !ok {
		t.Fatal("orphan discarded inside its grace period")
	}

	// Second pass past the grace window: the orphan is discarded.
	reaped, stale, _ = r.ReapOnce(now.Add(3 * time.Second))
	if reaped != 1 || stale != 0 {
		t.Fatalf("second pass: reaped=%d stale=%d, want 1/0", reaped, stale)
	}
	if _, ok := mgr.Get("scope:orphan"); ok {
		t.Fatal("orphan survived past grace period")
	}
}

func TestReapOnceSparesBridgedScope(t *testing.T) {
	r, mgr, runs, rootID := newReaperFixture(t)
	forkChild(t, mgr, rootID, "bridged")

	// Bridge the child the way a running dispatch does: an entry whose
	// steer channel exists and whose Done has not closed.
	runs.Register("scope:bridged", Run{SteerCh: make(chan string)})

	now := time.Now()
	for i := range 3 {
		reaped, _, _ := r.ReapOnce(now.Add(time.Duration(i+1) * 3 * time.Second))
		if reaped != 0 {
			t.Fatalf("pass %d: reaped %d, want 0 — a bridged run's scope is not an orphan", i+1, reaped)
		}
	}
	if _, ok := mgr.Get("scope:bridged"); !ok {
		t.Fatal("bridged scope was discarded")
	}
}

func TestReapOnceSparesPendingSourceScope(t *testing.T) {
	r, mgr, _, rootID := newReaperFixture(t)
	forkChild(t, mgr, rootID, "pending-proposal")

	// A retained proposal awaiting settlement keeps its scope active on
	// purpose; the reaper must treat it as live.
	r.SetPendingSource(func() []string { return []string{"scope:pending-proposal"} })

	now := time.Now()
	for i := range 3 {
		reaped, _, _ := r.ReapOnce(now.Add(time.Duration(i+1) * 3 * time.Second))
		if reaped != 0 {
			t.Fatalf("pass %d: reaped %d, want 0 — an unsettled proposal's scope is not an orphan", i+1, reaped)
		}
	}
	if _, ok := mgr.Get("scope:pending-proposal"); !ok {
		t.Fatal("pending-proposal scope was discarded")
	}
}

func TestReapOnceSparesRootScope(t *testing.T) {
	r, mgr, _, rootID := newReaperFixture(t)

	now := time.Now()
	for i := range 3 {
		reaped, _, _ := r.ReapOnce(now.Add(time.Duration(i+1) * 3 * time.Second))
		if reaped != 0 {
			t.Fatalf("pass %d: reaped %d, want 0 — the root scope is never an orphan", i+1, reaped)
		}
	}
	if _, ok := mgr.Get(rootID); !ok {
		t.Fatal("root scope was discarded")
	}
}

func TestReapOnceLedgerClearsWhenScopeReturnsToLive(t *testing.T) {
	r, mgr, runs, rootID := newReaperFixture(t)
	forkChild(t, mgr, rootID, "returns")

	now := time.Now()

	// Pass 1: unbridged, ledger records it.
	reaped, stale, _ := r.ReapOnce(now)
	if reaped != 0 || stale != 1 {
		t.Fatalf("pass 1: reaped=%d stale=%d, want 0/1", reaped, stale)
	}

	// Pass 2: bridged before grace expires — survives AND leaves the
	// ledger, so its grace clock restarts if it goes unbridged again.
	runs.Register("scope:returns", Run{SteerCh: make(chan string)})
	reaped, _, _ = r.ReapOnce(now.Add(1 * time.Second))
	if reaped != 0 {
		t.Fatalf("pass 2: reaped %d, want 0", reaped)
	}

	// Unbridge, then pass 3 one second later: the grace clock must have
	// restarted at pass 2, so the scope survives again.
	runs.Unregister("scope:returns")
	reaped, _, _ = r.ReapOnce(now.Add(2 * time.Second))
	if reaped != 0 {
		t.Fatalf("pass 3: reaped %d, want 0 — grace must restart after a scope returns to live", reaped)
	}

	// Pass 4 well past the restarted grace: now it is discarded.
	reaped, _, _ = r.ReapOnce(now.Add(6 * time.Second))
	if reaped != 1 {
		t.Fatalf("pass 4: reaped %d, want 1", reaped)
	}
	if _, ok := mgr.Get("scope:returns"); ok {
		t.Fatal("scope survived past restarted grace period")
	}
}

// TestReapOnceSweepsResumedScopeWithoutGrace pins failure mode two of
// yaah-95ri.7: a scope startup recovery re-admitted from the trace is
// swept on the first pass, not after a grace window it never earned. Its
// unbridged clock started before the process did, and nothing in this
// process will ever bridge it, so waiting only keeps dead work holding
// the dispatch cap.
func TestReapOnceSweepsResumedScopeWithoutGrace(t *testing.T) {
	r, mgr, _, rootID := newReaperFixture(t)
	forkChild(t, mgr, rootID, "resumed-dead")
	r.SetResumed([]string{"scope:resumed-dead"})

	now := time.Now()
	reaped, stale, _ := r.ReapOnce(now)
	if reaped != 1 || stale != 0 {
		t.Fatalf("first pass: reaped=%d stale=%d, want 1/0 — a resumed scope is past grace from the start", reaped, stale)
	}
	if _, ok := mgr.Get("scope:resumed-dead"); ok {
		t.Fatal("resumed scope survived the first pass")
	}
	// And the ledger forgets it: a retired scope must not leave a resumed
	// entry behind in a set that is never re-seeded.
	if r.resumed["scope:resumed-dead"] {
		t.Error("the resumed ledger still tracks a retired scope")
	}
}

// TestReapOnceSparesResumedLiveScopes pins the exemptions that still
// apply to a resumed scope: the session's own root (which a --resume
// reattaches and keeps working under) and a rehydrated proposal awaiting
// settlement (whose scope must stay active on purpose).
func TestReapOnceSparesResumedLiveScopes(t *testing.T) {
	r, mgr, _, rootID := newReaperFixture(t)
	forkChild(t, mgr, rootID, "resumed-proposal")
	r.SetResumed([]string{rootID, "scope:resumed-proposal"})
	r.SetPendingSource(func() []string { return []string{"scope:resumed-proposal"} })

	now := time.Now()
	for i := range 3 {
		reaped, _, _ := r.ReapOnce(now.Add(time.Duration(i) * time.Minute))
		if reaped != 0 {
			t.Fatalf("pass %d: reaped %d, want 0 — a live exemption outranks a resumed entry", i+1, reaped)
		}
	}
	if _, ok := mgr.Get(rootID); !ok {
		t.Fatal("the session's own root was reaped")
	}
	if _, ok := mgr.Get("scope:resumed-proposal"); !ok {
		t.Fatal("a resumed scope awaiting settlement was reaped")
	}
}

// TestStartSweepsResumedScopesBeforeTheFirstTick pins the case that made
// dispatch dead for a whole process (yaah-95ri.7): the sweep must happen
// at startup, not one interval later. The reaper's first tick is minutes
// away, and a session may not have that long before its first dispatch is
// refused by a cap full of resumed scopes.
func TestStartSweepsResumedScopesBeforeTheFirstTick(t *testing.T) {
	r, mgr, _, rootID := newReaperFixture(t)
	forkChild(t, mgr, rootID, "resumed-at-startup")
	r.SetResumed([]string{"scope:resumed-at-startup"})

	r.Start()

	if _, ok := mgr.Get("scope:resumed-at-startup"); ok {
		t.Fatal("Start must sweep resumed scopes before the first tick")
	}
}

// TestReapOnceRefusedDiscardRecordedOnce pins failure mode three of
// yaah-95ri.7: a scope the kernel PERMANENTLY refuses to discard is named once
// and never retried. The refusal comes from the trace's own reconstructed
// tree, so a retry cannot succeed — and an endless retry loop is what left the
// parent unable to close while the UI kept listing the scope.
func TestReapOnceRefusedDiscardRecordedOnce(t *testing.T) {
	r, mgr, _, rootID := newReaperFixture(t)
	forkChild(t, mgr, rootID, "tree-rejected")
	r.SetResumed([]string{"scope:tree-rejected"})

	attempts := 0
	r.discard = func(string) error {
		attempts++
		return fmt.Errorf("%w: scope is not reachable from the tree", errPermanentDiscard)
	}

	now := time.Now()
	for i := range 4 {
		reaped, _, _ := r.ReapOnce(now.Add(time.Duration(i) * time.Minute))
		if reaped != 0 {
			t.Fatalf("pass %d: reaped %d, want 0 — the discard was refused", i+1, reaped)
		}
	}
	if attempts != 1 {
		t.Fatalf("discard attempts = %d, want 1 — a refused scope must not be retried in a loop", attempts)
	}
	// Nothing is fabricated: the scope keeps whatever record it had.
	if _, ok := mgr.Get("scope:tree-rejected"); !ok {
		t.Fatal("a refused scope must keep its record")
	}
	if r.resumed["scope:tree-rejected"] {
		t.Error("a refused scope must leave the resumed ledger — it is refused, not resumed")
	}
}

// TestReapOnceTransientDiscardIsRetried pins the other half of the refusal
// policy: a failure that is not the kernel's tree refusal (a busy store, a
// deadline, an I/O fault) must not be recorded as permanent. Stranding an
// orphan scope on one bad sweep is a leak that lasts the process's whole life,
// so a transient error leaves the ledger clean and the scope staggers into the
// next sweep, which reaps it.
func TestReapOnceTransientDiscardIsRetried(t *testing.T) {
	r, mgr, _, rootID := newReaperFixture(t)
	forkChild(t, mgr, rootID, "flaky")

	attempts := 0
	inner := r.discard // the real one: the retry must actually land
	r.discard = func(id string) error {
		attempts++
		if attempts == 1 {
			return fmt.Errorf("record discard event: %w", context.DeadlineExceeded)
		}
		return inner(id)
	}

	now := time.Now()

	// Pass 1: the scope is unbridged but fresh — recorded, not attempted.
	if reaped, stale, _ := r.ReapOnce(now); reaped != 0 || stale != 1 {
		t.Fatalf("pass 1: reaped=%d stale=%d, want 0/1", reaped, stale)
	}
	if attempts != 0 {
		t.Fatalf("discard attempts = %d after the first pass, want 0 — grace has not expired", attempts)
	}

	// Pass 2 past grace: the discard fails transiently. The scope survives
	// and is NOT written off as refused.
	reaped, _, _ := r.ReapOnce(now.Add(3 * time.Second))
	if reaped != 0 {
		t.Fatalf("pass 2: reaped %d, want 0 — the discard failed", reaped)
	}
	if _, ok := mgr.Get("scope:flaky"); !ok {
		t.Fatal("a transiently failed discard dropped the scope's record")
	}
	if err, ok := r.refused["scope:flaky"]; ok {
		t.Fatalf("a transient failure was recorded as permanent: %v", err)
	}
	// The first failure is the one that warns; the counter is what keeps the
	// repeated sweeps at Debug.
	if got := r.transient["scope:flaky"]; got != 1 {
		t.Errorf("transient attempts after the first failure = %d, want 1", got)
	}

	// Pass 3: the retry succeeds and the scope is reaped.
	reaped, _, _ = r.ReapOnce(now.Add(6 * time.Second))
	if reaped != 1 {
		t.Fatalf("pass 3: reaped %d, want 1 — a transient failure must be retried", reaped)
	}
	if attempts != 2 {
		t.Fatalf("discard attempts = %d, want 2", attempts)
	}
	if _, ok := mgr.Get("scope:flaky"); ok {
		t.Fatal("the scope survived the sweep that retried successfully")
	}
	if err, ok := r.refused["scope:flaky"]; ok {
		t.Errorf("a successfully reaped scope stayed in the refused ledger: %v", err)
	}
	// A reaped scope takes its retry counter with it.
	if got, ok := r.transient["scope:flaky"]; ok {
		t.Errorf("a reaped scope left a retry counter behind: %d", got)
	}
}

// TestClassifyDiscard pins the phrase table the retry policy rests on. The
// permanent set is deliberately narrow: only a tree-shape refusal — something
// the append-only trace cannot change between sweeps — may strand a scope.
// Everything else stays retryable, including the "cannot discard" refusal a
// parent gets while a child is still active (the retry is the next sweep, after
// the child reaps).
func TestClassifyDiscard(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantPerm bool
	}{
		{
			name:     "kernel tree refusal is permanent",
			err:      errors.New("discard scope: scope:abc is not a child of scope:def"),
			wantPerm: true,
		},
		{
			name:     "root scope refusal is permanent",
			err:      errors.New("discard scope: scope:root has no parent (root scope)"),
			wantPerm: true,
		},
		{
			name: "deadline is transient",
			err:  errors.New("record discard event: context deadline exceeded"),
		},
		{
			name: "not found is transient",
			err:  errors.New("discard scope: not found"),
		},
		{
			name: "active children refusal is transient",
			err:  errors.New("cannot discard scope: 1 active children"),
		},
		{
			name: "cannot discard from scope is transient",
			err:  errors.New("cannot discard scope:abc from scope:def"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyDiscard(tt.err)
			if permanent := errors.Is(got, errPermanentDiscard); permanent != tt.wantPerm {
				t.Fatalf("classifyDiscard(%q) permanent = %v, want %v", tt.err, permanent, tt.wantPerm)
			}
			if !errors.Is(got, tt.err) {
				t.Errorf("classifyDiscard(%q) dropped the original error from the chain", tt.err)
			}
			if !tt.wantPerm && got.Error() != tt.err.Error() {
				t.Errorf("classifyDiscard(%q) = %q, want the transient error passed through unchanged", tt.err, got)
			}
		})
	}

	t.Run("nil passes through", func(t *testing.T) {
		if got := classifyDiscard(nil); got != nil {
			t.Errorf("classifyDiscard(nil) = %v, want nil", got)
		}
	})

	t.Run("an already classified error is not wrapped twice", func(t *testing.T) {
		inner := fmt.Errorf("%w: scope is not reachable from the tree", errPermanentDiscard)
		got := classifyDiscard(inner)
		if got != inner {
			t.Errorf("classifyDiscard returned %v, want the error untouched", got)
		}
	})
}

// TestReapOnceReapsDeadSubtreeChildFirst pins the tree half of
// yaah-95ri.7: a resumed subtree is swept whole and children-first — a
// parent discarded while its child is still active is refused, which would
// leave the parent permanent and the child orphaned under it.
func TestReapOnceReapsDeadSubtreeChildFirst(t *testing.T) {
	r, mgr, _, rootID := newReaperFixture(t)
	mid, err := mgr.Fork(rootID, "sub-mid", nil)
	if err != nil {
		t.Fatalf("fork mid: %v", err)
	}
	leaf, err := mgr.Fork(mid.ID(), "sub-leaf", nil)
	if err != nil {
		t.Fatalf("fork leaf: %v", err)
	}
	// Deepest-first: the resumed set is unordered on purpose.
	r.SetResumed([]string{mid.ID(), leaf.ID()})

	reaped, _, _ := r.ReapOnce(time.Now())
	if reaped != 2 {
		t.Fatalf("reaped = %d, want 2 — a dead subtree is reaped whole", reaped)
	}
	for _, id := range []string{leaf.ID(), mid.ID()} {
		if _, ok := mgr.Get(id); ok {
			t.Fatalf("%s survived the resume sweep", id)
		}
	}
	if _, ok := mgr.Get(rootID); !ok {
		t.Fatal("this session's root was reaped")
	}
}
