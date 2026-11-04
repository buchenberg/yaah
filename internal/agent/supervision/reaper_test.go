package supervision

import (
	"context"
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
