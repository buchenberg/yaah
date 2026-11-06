package dispatch

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/yaah/internal/jobs"
)

// capTestManager builds a real ScopeManager over a temp SQLite trace store
// with a pure-causal root scope — the same substrate shape the dispatch
// lanes fork from.
func capTestManager(t *testing.T) (*goatherd.ScopeManager, string) {
	t.Helper()
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	mgr := goatherd.NewScopeManager(store)
	root, err := mgr.Create("cap-test-root", nil)
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	return mgr, root.ID()
}

func TestDispatchScopeCapExceededBelowCap(t *testing.T) {
	mgr, rootID := capTestManager(t)
	rt := RetainedRuntime{ScopeManager: mgr, RootScopeID: rootID}
	if err := dispatchScopeCapExceeded(rt); err != nil {
		t.Fatalf("below cap: dispatchScopeCapExceeded = %v, want nil", err)
	}
}

func TestDispatchScopeCapExceededAtCap(t *testing.T) {
	mgr, rootID := capTestManager(t)
	rt := RetainedRuntime{ScopeManager: mgr, RootScopeID: rootID, MaxDispatchScopes: 2}
	for _, id := range []string{"filler-1", "filler-2"} {
		if _, err := mgr.Fork(rootID, id, nil); err != nil {
			t.Fatalf("fork %s: %v", id, err)
		}
	}
	err := dispatchScopeCapExceeded(rt)
	if err == nil {
		t.Fatal("at cap: dispatchScopeCapExceeded = nil, want ErrScopeExhausted")
	}
	if !errors.Is(err, jobs.ErrScopeExhausted) {
		t.Fatalf("at cap: error does not match jobs.ErrScopeExhausted: %v", err)
	}
	// The refusal must never be confusable with a watchdog verdict.
	if errors.Is(err, ErrStuckChild) || errors.Is(err, ErrDispatchStartup) {
		t.Fatalf("cap refusal misclassified as a watchdog error: %v", err)
	}
}

func TestDispatchScopeCapExceededNilManager(t *testing.T) {
	rt := RetainedRuntime{}
	if err := dispatchScopeCapExceeded(rt); err != nil {
		t.Fatalf("nil manager: dispatchScopeCapExceeded = %v, want nil", err)
	}
}

// TestDispatchScopeCapExceededIgnoresResumedScopes pins the container /
// worktree lane's half of yaah-95ri.7: the scopes startup recovery
// re-admitted from the trace hold no loop in this process, so they must
// not be the reason a live dispatch is refused after a crash.
func TestDispatchScopeCapExceededIgnoresResumedScopes(t *testing.T) {
	mgr, rootID := capTestManager(t)
	rt := RetainedRuntime{ScopeManager: mgr, RootScopeID: rootID, MaxDispatchScopes: 2}
	for _, id := range []string{"filler-1", "filler-2"} {
		if _, err := mgr.Fork(rootID, id, nil); err != nil {
			t.Fatalf("fork %s: %v", id, err)
		}
	}
	// At the cap while every filler looks live (root + 2 fillers = 3 >= 2).
	if err := dispatchScopeCapExceeded(rt); err == nil {
		t.Fatal("precondition: the cap must refuse while the fillers count")
	}
	// The same tree, with the fillers marked as resumed: capacity is free.
	rt.ResumedScopes = map[string]bool{"scope:filler-1": true, "scope:filler-2": true}
	if err := dispatchScopeCapExceeded(rt); err != nil {
		t.Fatalf("resumed scopes must not consume the cap, got %v", err)
	}
}

func TestRetainedRuntimePendingProposalScopeIDsNilSafe(t *testing.T) {
	rt := RetainedRuntime{}
	if got := rt.PendingProposalScopeIDs(); got != nil {
		t.Fatalf("zero-value runtime: PendingProposalScopeIDs = %v, want nil", got)
	}
}
