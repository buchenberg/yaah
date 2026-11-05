package recovery

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"

	"github.com/buchenberg/yaah/internal/jobs"
)

// seedCrashedDispatchTrace writes a crashed session's scope tree into a
// fresh trace store: an ACTIVE root and n ACTIVE children — the durable
// state a killed process leaves behind, where every record still says
// ACTIVE and no execution backs any of them. It returns the store path
// and the root's scope ID.
func seedCrashedDispatchTrace(t *testing.T, children int) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mgr := goatherd.NewScopeManager(store)
	root, err := mgr.Create("sess-crashed", nil)
	if err != nil {
		store.Close(context.Background())
		t.Fatalf("create root: %v", err)
	}
	for i := range children {
		if _, ferr := mgr.Fork(root.ID(), fmt.Sprintf("sub-crashed-%d", i), nil); ferr != nil {
			store.Close(context.Background())
			t.Fatalf("fork child %d: %v", i, ferr)
		}
	}
	if cerr := store.Close(context.Background()); cerr != nil {
		t.Fatalf("close store: %v", cerr)
	}
	return path, root.ID()
}

// recoverFromTrace reopens the store the way a restarted process does and
// runs the recovery these tests exercise. adoptRoots is off: this trace
// carries no worktree fork records, so the recovery path under test needs
// no git repository.
func recoverFromTrace(t *testing.T, path string) (*goatherd.ScopeManager, *Report) {
	t.Helper()
	store, err := shepherdtrace.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	mgr, report, err := Recover(context.Background(), store, t.TempDir(), false, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	return mgr, report
}

// TestRecover_ResumedScopesRegisteredInTree pins failure mode one of
// yaah-95ri.7: the scopes a restart re-admits from the trace are real
// entries in the rebuilt tree — registered, with their parent re-hung —
// and recovery reports them as resumed so the caller can treat them as
// execution-less instead of as live work. A resumed child whose parent is
// unresolvable is the bug this rules out: the parent could never close it.
func TestRecover_ResumedScopesRegisteredInTree(t *testing.T) {
	const children = 3
	path, rootID := seedCrashedDispatchTrace(t, children)

	mgr, report := recoverFromTrace(t, path)

	if len(report.Resumed) != children+1 {
		t.Fatalf("report.Resumed has %d entries, want %d (the root and its children)", len(report.Resumed), children+1)
	}
	byID := make(map[string]Resumed, len(report.Resumed))
	for _, resumed := range report.Resumed {
		byID[resumed.ScopeID] = resumed
	}
	for i := range children {
		id := fmt.Sprintf("scope:sub-crashed-%d", i)
		resumed, ok := byID[id]
		if !ok {
			t.Fatalf("%s was not reported as resumed (report: %+v)", id, report.Resumed)
		}
		if resumed.Parent != rootID {
			t.Errorf("%s resumed parent = %q, want %q — the rebuilt tree must re-hang the parent", id, resumed.Parent, rootID)
		}
		if resumed.Outcome != ResumedExecution {
			t.Errorf("%s outcome = %q, want %q", id, resumed.Outcome, ResumedExecution)
		}
		// The report must describe the tree, not a wish: the scope is
		// registered in the manager with the same parent.
		scope, ok := mgr.Get(id)
		if !ok || scope.State() != goatherd.ScopeActive {
			t.Fatalf("%s not recovered active (ok=%v)", id, ok)
		}
		if parent := scope.Parent(); parent == nil || parent.ID() != rootID {
			t.Errorf("%s is not registered under the root in the rebuilt tree", id)
		}
	}
	root, ok := byID[rootID]
	if !ok {
		t.Fatalf("the recovered root was not reported as resumed (report: %+v)", report.Resumed)
	}
	if root.Parent != "" {
		t.Errorf("root resumed parent = %q, want empty", root.Parent)
	}
}

// TestRecover_ResumedScopesDoNotConsumeDispatchCap pins failure mode four
// of yaah-95ri.7 through the real trace-recovery path: a trace holding
// more active scopes than the dispatch cap, re-admitted by a restart, used
// to leave the new process unable to dispatch at all — and every further
// restart re-admitted the same dead scopes, so no restart cleared it. The
// cap counts scopes that can still execute; resumed scopes cannot.
func TestRecover_ResumedScopesDoNotConsumeDispatchCap(t *testing.T) {
	// Past the default cap (64): the exact state that made dispatch dead.
	children := jobs.DefaultMaxDispatchScopes + 8
	path, rootID := seedCrashedDispatchTrace(t, children)

	mgr, report := recoverFromTrace(t, path)

	// The rebuilt tree really is past the cap — the reproduction is not
	// vacuous.
	active := mgr.ActiveScopes()
	if len(active) <= jobs.DefaultMaxDispatchScopes {
		t.Fatalf("recovered %d active scopes, want more than the cap %d for the reproduction", len(active), jobs.DefaultMaxDispatchScopes)
	}
	// Counted the way the dispatch paths count: every one of them is
	// resumed, so none consumes capacity.
	if n := jobs.DispatchScopeCount(active, ResumedIDs(report.Resumed)); n != 0 {
		t.Fatalf("dispatch scope count = %d, want 0 — resumed scopes must not hold dispatch capacity", n)
	}
	// The root of the recovered tree is one of them, not a special case: a
	// previous session's root is as dead as its children.
	if !ResumedIDs(report.Resumed)[rootID] {
		t.Errorf("the recovered root %s is not in the resumed set", rootID)
	}
}

// TestResumedIDs_NilForEmpty pins the "absent means none" contract: the
// cap accounting and the reaper both test the set for nil/emptiness, and
// an allocated empty map would be a slower way to say nothing.
func TestResumedIDs_NilForEmpty(t *testing.T) {
	if got := ResumedIDs(nil); got != nil {
		t.Fatalf("ResumedIDs(nil) = %v, want nil", got)
	}
	ids := ResumedIDs([]Resumed{{ScopeID: "scope:a"}, {ScopeID: "scope:b"}})
	if len(ids) != 2 || !ids["scope:a"] || !ids["scope:b"] {
		t.Fatalf("ResumedIDs = %v, want both scope IDs", ids)
	}
}
