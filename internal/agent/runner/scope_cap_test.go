package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/yaah/internal/agent/recovery"
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

// TestCreateSubAgentScopeIgnoresResumedScopesAtCap pins failure mode four
// of yaah-95ri.7 end to end, through the real trace-recovery path: a trace
// holding more active scopes than the cap, re-admitted by a restart, must
// not refuse the restarted process's first dispatch — and must not keep
// refusing on every restart after it. Resumed scopes have no loop behind
// them in this process, so they hold no dispatch capacity either.
func TestCreateSubAgentScopeIgnoresResumedScopesAtCap(t *testing.T) {
	const children = jobs.DefaultMaxDispatchScopes + 4

	// The crashed run: a root and more active children than the cap allows,
	// recorded durably — this is what a killed process leaves in the trace.
	path := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	seed := goatherd.NewScopeManager(store)
	root, err := seed.Create("sess-crashed", nil)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	for i := range children {
		if _, ferr := seed.Fork(root.ID(), fmt.Sprintf("sub-crashed-%d", i), nil); ferr != nil {
			t.Fatalf("fork child %d: %v", i, ferr)
		}
	}
	if cerr := store.Close(context.Background()); cerr != nil {
		t.Fatalf("close store: %v", cerr)
	}

	// The restart: recovery rebuilds the very same tree from the trace.
	reopened, err := shepherdtrace.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { reopened.Close(context.Background()) })
	mgr, report, err := recovery.Recover(context.Background(), reopened, t.TempDir(), false, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	opts := taskRunnerOpts{
		rootScopeID:   root.ID(),
		scopeManager:  mgr,
		resumedScopes: recovery.ResumedIDs(report.Resumed),
	}

	t.Run("counting raw active scopes is the bug", func(t *testing.T) {
		// Without the resumed exclusion the same recovered tree refuses the
		// dispatch outright: this is the dead session the fix removes.
		bare := opts
		bare.resumedScopes = nil
		_, err := createSubAgentScope(mgr, bare, "sub-without-exclusion", "")
		if !errors.Is(err, jobs.ErrScopeExhausted) {
			t.Fatalf("without the resumed exclusion: error = %v, want ErrScopeExhausted", err)
		}
	})

	t.Run("resumed scopes hold no capacity", func(t *testing.T) {
		scope, err := createSubAgentScope(mgr, opts, "sub-after-restart", "")
		if err != nil {
			t.Fatalf("first dispatch after recovery = %v, want a scope", err)
		}
		if scope == nil || scope.Parent() == nil || scope.Parent().ID() != root.ID() {
			t.Fatalf("the dispatch did not fork under the recovered root: %+v", scope)
		}
	})
}
