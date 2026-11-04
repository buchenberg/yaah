package runner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/yaah/internal/jobs"
)

// scopeCapFixture builds a real ScopeManager over a temp SQLite trace
// store with a pure-causal root scope — the substrate shape every
// runner-side dispatch forks from.
func scopeCapFixture(t *testing.T) (*goatherd.ScopeManager, string) {
	t.Helper()
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	mgr := goatherd.NewScopeManager(store)
	root, err := mgr.Create("runner-cap-root", nil)
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	return mgr, root.ID()
}

func TestCreateSubAgentScopeRefusesAtCap(t *testing.T) {
	mgr, rootID := scopeCapFixture(t)
	opts := taskRunnerOpts{
		rootScopeID:       rootID,
		scopeManager:      mgr,
		maxDispatchScopes: 1,
	}
	if _, err := mgr.Fork(rootID, "scope:filler", nil); err != nil {
		t.Fatalf("fork filler: %v", err)
	}
	_, err := createSubAgentScope(mgr, opts, "sub-trace", "")
	if err == nil {
		t.Fatal("at cap: createSubAgentScope = nil error, want ErrScopeExhausted")
	}
	if !errors.Is(err, jobs.ErrScopeExhausted) {
		t.Fatalf("at cap: createSubAgentScope error does not match jobs.ErrScopeExhausted: %v", err)
	}
	// The refusal must never be confusable with a watchdog verdict: it
	// fires before any world is provisioned and before the watchdog arms.
	if errors.Is(err, jobs.ErrStuckChild) || errors.Is(err, jobs.ErrDispatchStartup) {
		t.Fatalf("cap refusal misclassified as a watchdog error: %v", err)
	}
}

func TestCreateSubAgentScopeCreatesBelowCap(t *testing.T) {
	mgr, rootID := scopeCapFixture(t)
	opts := taskRunnerOpts{
		rootScopeID:       rootID,
		scopeManager:      mgr,
		maxDispatchScopes: 3, // root + first dispatch + one filler
	}
	sc, err := createSubAgentScope(mgr, opts, "sub-trace", "")
	if err != nil {
		t.Fatalf("below cap: createSubAgentScope = %v, want a scope", err)
	}
	if sc == nil {
		t.Fatal("below cap: createSubAgentScope returned a nil scope")
	}
	// Bring the tree to the cap with one more active scope, then confirm
	// the next dispatch is refused.
	if _, err := mgr.Fork(rootID, "filler", nil); err != nil {
		t.Fatalf("fork filler: %v", err)
	}
	_, err = createSubAgentScope(mgr, opts, "sub-trace-2", "")
	if err == nil {
		t.Fatal("dispatch at cap was not refused")
	}
	if !errors.Is(err, jobs.ErrScopeExhausted) {
		t.Fatalf("dispatch at cap: error does not match jobs.ErrScopeExhausted: %v", err)
	}
}
