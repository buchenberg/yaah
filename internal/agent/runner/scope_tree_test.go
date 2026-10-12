package runner

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"

	"github.com/buchenberg/yaah/internal/agent/recovery"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

// newScopeManagerForTest returns a scope manager over an in-memory store.
func newScopeManagerForTest(t *testing.T) (*shepherd.ScopeManager, *shepherdtrace.SQLiteTraceStore) {
	t.Helper()
	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	return shepherd.NewScopeManager(store), store
}

// TestCreateRootScope_LocalAndIsolated pins the D1 root substrate choice:
// a local session's root carries the shared in-place git sandbox; an
// isolated session's root is pure-causal — the container is the substrate,
// and host-git capture would record a tree the session never works on.
func TestCreateRootScope_LocalAndIsolated(t *testing.T) {
	repo := newRunnerTestGitRepo(t)
	mgr, _ := newScopeManagerForTest(t)

	local, err := CreateRootScope(mgr, "sess-local", repo, false)
	if err != nil {
		t.Fatalf("CreateRootScope (local): %v", err)
	}
	// The behavioral contract, not the kernel's internal ID format: the
	// root is owned by the session (so scope IDs and trace owners line
	// up) and addressable through the manager.
	if local.OwnerID() != "sess-local" {
		t.Errorf("root owner = %q, want the session ID", local.OwnerID())
	}
	if got, ok := mgr.Get(local.ID()); !ok || got != local {
		t.Errorf("mgr.Get(root.ID()) roundtrip failed: %v, %v", got, ok)
	}
	if sb := local.Sandbox(); sb == nil || sb.Backend() != "git" {
		t.Errorf("local root sandbox = %#v, want the in-place git backend", sb)
	}

	// A directory git cannot capture (not a repository) demotes the root
	// to pure-causal: Fork captures the parent baseline first, so an
	// uncapturable root would break every child fork.
	nonGit := t.TempDir()
	probe, err := CreateRootScope(mgr, "sess-nongit", nonGit, false)
	if err != nil {
		t.Fatalf("CreateRootScope (non-git): %v", err)
	}
	if sb := probe.Sandbox(); sb != nil {
		t.Errorf("non-git root sandbox = %T, want pure-causal nil (capture probe failed)", sb)
	}

	iso, err := CreateRootScope(mgr, "sess-iso", repo, true)
	if err != nil {
		t.Fatalf("CreateRootScope (isolated): %v", err)
	}
	if sb := iso.Sandbox(); sb != nil {
		t.Errorf("isolated root sandbox = %v (%T), want pure-causal nil", sb, sb)
	}

	// The bare case falls back to the process working directory,
	// matching subAgentSandbox's resolution — the root and the legacy
	// child scopes must agree on the repository.
	bare, err := CreateRootScope(mgr, "sess-bare", "", false)
	if err != nil {
		t.Fatalf("CreateRootScope (bare): %v", err)
	}
	if sb := bare.Sandbox(); sb == nil || sb.Backend() != "git" {
		t.Errorf("bare root sandbox = %#v, want the cwd git backend", sb)
	}
}

// TestCreateSubAgentScope_ForksFromRoot pins the tree: with a root scope
// set, a plain dispatch forks it (parent linkage, inherited sandbox) and
// the fork lands in the trace as a scope:forked record — the causality
// recovery and the supervisor's tree view both rely on.
func TestCreateSubAgentScope_ForksFromRoot(t *testing.T) {
	mgr, store := newScopeManagerForTest(t)
	root, err := CreateRootScope(mgr, "sess-tree", "", true) // pure-causal root
	if err != nil {
		t.Fatalf("CreateRootScope: %v", err)
	}
	opts := taskRunnerOpts{rootScopeID: root.ID()}

	child, err := createSubAgentScope(mgr, opts, "sub-dev-1", "")
	if err != nil {
		t.Fatalf("createSubAgentScope: %v", err)
	}
	if child.Parent() == nil || child.Parent().ID() != root.ID() {
		t.Fatalf("child parent = %#v, want the root scope", child.Parent())
	}
	if sb := child.Sandbox(); sb != nil {
		t.Errorf("child of a pure-causal root must stay pure-causal, got %T", sb)
	}

	// The fork-point record lands in the PARENT's trace (kernel contract:
	// "record the fork event in the parent's trace") with the child named
	// in the payload — recovery and the supervisor's tree view both rely
	// on it.
	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "sess-tree", 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("ReadOwnerPrefix: %v", err)
	}
	var forked bool
	for _, factID := range slice.FactIDs() {
		if v := slice.FactsByID[factID].GetView(); v != nil && v.KindLabel == "scope:forked" {
			forked = true
		}
	}
	if !forked {
		t.Errorf("the parent's trace must record the fork (scope:forked), got %d facts", len(slice.FactIDs()))
	}
}

// TestCreateSubAgentScope_FallbackWithoutRoot keeps the pre-plan path
// pinned: with no root scope (tracing up, root creation refused), a
// dispatch still gets a superviseable flat scope.
func TestCreateSubAgentScope_FallbackWithoutRoot(t *testing.T) {
	mgr, _ := newScopeManagerForTest(t)
	opts := taskRunnerOpts{defaults: config.Defaults{SupervisedRepoPath: newRunnerTestGitRepo(t)}}

	child, err := createSubAgentScope(mgr, opts, "sub-flat-1", "")
	if err != nil {
		t.Fatalf("createSubAgentScope: %v", err)
	}
	if child.Parent() != nil {
		t.Errorf("legacy flat scope must have no parent, got %s", child.Parent().ID())
	}
	if sb := child.Sandbox(); sb == nil || sb.Backend() != "git" {
		t.Errorf("legacy flat scope should carry subAgentSandbox's git backend, got %#v", sb)
	}
}

// TestCreateSubAgentScope_WorkdirForksIsolated pins the workdir branch: a
// workdir dispatch forks in isolation with its own worktree sandbox — the
// worktree is destroyed by the kernel on halt/discard, exactly as the
// pre-plan Create+WorktreeSandbox path was.
func TestCreateSubAgentScope_WorkdirForksIsolated(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	repo := newRunnerTestGitRepo(t)
	mgr, store := newScopeManagerForTest(t)
	// The workdir branch records the worktree path for recovery (R1)
	// through the shared store global, exactly as the wiring pairs it
	// with the shared manager.
	prevStore := tools.SharedTraceStore
	tools.SharedTraceStore = store
	t.Cleanup(func() { tools.SharedTraceStore = prevStore })
	root, err := CreateRootScope(mgr, "sess-wt", repo, false)
	if err != nil {
		t.Fatalf("CreateRootScope: %v", err)
	}
	opts := taskRunnerOpts{
		rootScopeID: root.ID(),
		defaults:    config.Defaults{SupervisedRepoPath: repo},
	}

	child, err := createSubAgentScope(mgr, opts, "sub-wt-1", filepath.Join(t.TempDir(), "wt-branch"))
	if err != nil {
		t.Fatalf("createSubAgentScope (workdir): %v", err)
	}
	if child.Parent() == nil || child.Parent().ID() != root.ID() {
		t.Fatalf("workdir child parent = %#v, want the root scope", child.Parent())
	}
	sb := child.Sandbox()
	if sb == nil {
		t.Fatal("workdir child must own a sandbox")
	}
	// Backend() is "git" for both variants; Capabilities().Isolated is
	// the exported worktree discriminator — the property that matters,
	// since an isolated sandbox is destroyed on halt/discard.
	if caps := sb.Capabilities(); !caps.Isolated {
		t.Errorf("workdir child capabilities = %#v, want an isolated (worktree) sandbox", caps)
	}
	if sb == root.Sandbox() {
		t.Error("the workdir child must own its sandbox, not inherit the root's")
	}
}

// TestCreateSubAgentScope_IsolatedIgnoresWorkdir pins step-9 semantics in
// the new fork path: an isolated session ignores workdir — the dispatch
// is a plain fork, and since the isolated root is pure-causal the child is
// too. The container's root is every sub-agent's workspace.
func TestCreateSubAgentScope_IsolatedIgnoresWorkdir(t *testing.T) {
	mgr, _ := newScopeManagerForTest(t)
	root, err := CreateRootScope(mgr, "sess-iso2", "", true)
	if err != nil {
		t.Fatalf("CreateRootScope: %v", err)
	}
	opts := taskRunnerOpts{
		rootScopeID: root.ID(),
		workspace:   fakeSandboxWorkspace{},
	}

	child, err := createSubAgentScope(mgr, opts, "sub-iso-1", "/host/some/worktree")
	if err != nil {
		t.Fatalf("createSubAgentScope: %v", err)
	}
	if child.Parent() == nil || child.Parent().ID() != root.ID() {
		t.Fatalf("child parent = %#v, want the root scope", child.Parent())
	}
	if sb := child.Sandbox(); sb != nil {
		t.Errorf("isolated child must stay pure-causal, got %T — workdir must not conjure a host worktree", sb)
	}
}

// TestCreateRootScope_ResumeReattachesRecoveredRoot pins the R2 resume
// path: --resume continues under the same session ID, so a recovered
// registry already holds the crashed run's root — CreateRootScope
// reattaches it (with its recovered sandbox) instead of colliding. A
// fresh session ID still creates; a halted recovered root refuses
// loudly instead of degrading silently.
func TestCreateRootScope_ResumeReattachesRecoveredRoot(t *testing.T) {
	repo := newRunnerTestGitRepo(t)
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	tracePath := filepath.Join(t.TempDir(), "trace.sqlite")
	store.Close(context.Background())
	store, err = shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	// The crashed run: a root with a forked child (a root that never
	// forked leaves no trace record).
	crashed := shepherd.NewScopeManager(store)
	root, err := CreateRootScope(crashed, "sess-resume", repo, false)
	if err != nil {
		t.Fatalf("CreateRootScope: %v", err)
	}
	if _, err := crashed.Fork(root.ID(), "sub-resumed", nil); err != nil {
		t.Fatalf("fork: %v", err)
	}
	store.Close(context.Background())

	// The restart: recovery rebuilds, resume reattaches.
	store2, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { store2.Close(context.Background()) })
	recovered, _, err := recovery.Recover(context.Background(), store2, repo, true, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	reattached, err := CreateRootScope(recovered, "sess-resume", repo, false)
	if err != nil {
		t.Fatalf("resume CreateRootScope: %v", err)
	}
	if reattached.ID() != root.ID() {
		t.Fatalf("resume must reattach the recovered root: got %s, want %s", reattached.ID(), root.ID())
	}
	if reattached.Sandbox() == nil {
		t.Error("the reattached root must carry its recovered sandbox")
	}

	// A fresh session ID still creates its own root.
	fresh, err := CreateRootScope(recovered, "sess-fresh", repo, false)
	if err != nil {
		t.Fatalf("fresh CreateRootScope: %v", err)
	}
	if fresh.ID() == root.ID() {
		t.Error("a fresh session must not reuse the resumed root")
	}
}

// TestCreateRootScope_HaltedRecoveredRootRefuses pins the loud
// refusal: a root the crashed run halted is terminal — reattaching it
// would resume a session whose scope-tree supervision already ended,
// and silently degrading to flat scopes is the old bug.
func TestCreateRootScope_HaltedRecoveredRootRefuses(t *testing.T) {
	repo := newRunnerTestGitRepo(t)
	tracePath := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	crashed := shepherd.NewScopeManager(store)
	root, err := CreateRootScope(crashed, "sess-halted", repo, false)
	if err != nil {
		t.Fatalf("CreateRootScope: %v", err)
	}
	if _, err := crashed.Fork(root.ID(), "sub-halted", nil); err != nil {
		t.Fatalf("fork: %v", err)
	}
	if err := root.Halt(); err != nil {
		t.Fatalf("halt: %v", err)
	}
	store.Close(context.Background())

	store2, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { store2.Close(context.Background()) })
	recovered, _, err := recovery.Recover(context.Background(), store2, repo, true, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if _, err := CreateRootScope(recovered, "sess-halted", repo, false); err == nil {
		t.Fatal("reattaching a halted root must refuse loudly")
	}
}

// TestCreateSubAgentScope_HaltedRootRefusesBothPaths pins plan 07 H6's
// policy: a dispatch that arrives after the supervisor halt made the
// root terminal fails loudly with ErrRootTerminal on BOTH tree-escaping
// paths — the plain fork and the workdir ForkIsolated — instead of
// silently degrading to a flat scope outside the session tree, which
// recovery would rebuild as a sibling of a dead session.
func TestCreateSubAgentScope_HaltedRootRefusesBothPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	repo := newRunnerTestGitRepo(t)

	for _, tc := range []struct {
		name    string
		workdir string
	}{
		{"plain fork", ""},
		{"workdir isolated fork", filepath.Join(t.TempDir(), "wt-halted")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _ := newScopeManagerForTest(t)
			root, err := CreateRootScope(mgr, "sess-halt-"+strings.ReplaceAll(tc.name, " ", "-"), repo, false)
			if err != nil {
				t.Fatalf("CreateRootScope: %v", err)
			}
			// The halt: A3's session-level kill ends the root's
			// lifecycle.
			if err := root.Halt(); err != nil {
				t.Fatalf("halt root: %v", err)
			}
			opts := taskRunnerOpts{
				rootScopeID: root.ID(),
				defaults:    config.Defaults{SupervisedRepoPath: repo},
			}

			_, err = createSubAgentScope(mgr, opts, "sub-after-halt", tc.workdir)
			if !errors.Is(err, ErrRootTerminal) {
				t.Fatalf("dispatch after halt: %v, want ErrRootTerminal (the halt is a session-level kill; the dispatch must refuse, not degrade)", err)
			}
			if !strings.Contains(err.Error(), "start a new session") {
				t.Errorf("the refusal must be actionable, got: %v", err)
			}
		})
	}
}

// TestCreateSubAgentScope_NonTerminalFailureDegradesLoudly pins the other
// half of the policy: a fork failure that is NOT the halted root still
// degrades toward the flat scope — the sub-agent stays superviseable —
// but the degradation is logged, not silent. The forced failure is a
// child ID collision, the one non-terminal failure a healthy root can
// produce; the collision also fails the flat Create fallback, so the
// dispatch fails honestly — what is pinned here is that the degradation
// warning fired on the way down, naming the root and the sub.
func TestCreateSubAgentScope_NonTerminalFailureDegradesLoudly(t *testing.T) {
	mgr, _ := newScopeManagerForTest(t)
	root, err := CreateRootScope(mgr, "sess-degrade", "", true) // pure-causal root
	if err != nil {
		t.Fatalf("CreateRootScope: %v", err)
	}
	opts := taskRunnerOpts{rootScopeID: root.ID()}

	// Capture the default logger so the degradation warning is
	// observable, not just emitted.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if _, err := createSubAgentScope(mgr, opts, "sub-collide", ""); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	_, err = createSubAgentScope(mgr, opts, "sub-collide", "")
	if err == nil {
		t.Fatal("a duplicate dispatch must fail; the scope already exists")
	}
	if errors.Is(err, ErrRootTerminal) {
		t.Fatalf("a child ID collision is not a terminal root; got: %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "degrading to a flat scope outside the session tree") {
		t.Errorf("the degradation must be logged, got: %q", logged)
	}
	if !strings.Contains(logged, "sub-collide") || !strings.Contains(logged, root.ID()) {
		t.Errorf("the warning must name the root and the sub-agent, got: %q", logged)
	}
}

// TestCreateSubAgentScope_TracingOffStaysFlat re-pins the untouched
// degraded mode: with no root scope (tracing up, root creation refused),
// a dispatch still gets its flat superviseable scope — the one fallback
// H6 deliberately leaves silent.
func TestCreateSubAgentScope_TracingOffStaysFlat(t *testing.T) {
	mgr, _ := newScopeManagerForTest(t)
	opts := taskRunnerOpts{defaults: config.Defaults{SupervisedRepoPath: newRunnerTestGitRepo(t)}}

	child, err := createSubAgentScope(mgr, opts, "sub-flat-off", "")
	if err != nil {
		t.Fatalf("createSubAgentScope (tracing off): %v", err)
	}
	if child.Parent() != nil {
		t.Errorf("tracing-off scope must be flat, got parent %s", child.Parent().ID())
	}
}
