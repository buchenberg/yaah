package recovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"
	shepherdgit "github.com/buchenberg/shepherd-kernel-go/sandbox/git"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
)

// newGitRepo creates a committed repository — worktrees and captures
// both need a HEAD.
func newGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "gitconfig-system"))
	mustGit(t, dir, "init")
	mustGit(t, dir, "config", "user.email", "test@test.com")
	mustGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-m", "init")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", args[0], dir, err, out)
	}
}

// crashState builds one crashed session's state against a file-backed
// store: a root, a checkpointed shared child, a discarded child, and
// an isolated worktree child that sealed an output. It returns the
// identifiers recovery must hand back.
type crashState struct {
	storePath       string
	repo            string
	checkpointID    string
	outputID        string
	sharedChildID   string
	discardedID     string
	worktreeChildID string
	worktreePath    string
}

func buildCrashState(t *testing.T) *crashState {
	t.Helper()
	repo := newGitRepo(t)
	dir := t.TempDir()
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(dir, "trace.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	state := &crashState{storePath: filepath.Join(dir, "trace.sqlite"), repo: repo}
	t.Cleanup(func() { store.Close(context.Background()) })

	mgr := shepherd.NewScopeManager(store)
	root, err := mgr.Create("sess-old", shepherdgit.NewLocalGitSandbox(repo))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}

	// A shared child with a live checkpoint.
	shared, err := mgr.Fork(root.ID(), "sub-shared", nil)
	if err != nil {
		t.Fatalf("fork shared child: %v", err)
	}
	state.sharedChildID = shared.ID()
	cp, err := mgr.CreateCheckpoint(context.Background(), shared.ID(), []byte(`[{"role":"assistant","content":"unit one"}]`))
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	state.checkpointID = cp.ID

	// A discarded child (terminal state is data too).
	dead, err := mgr.Fork(root.ID(), "sub-dead", nil)
	if err != nil {
		t.Fatalf("fork dead child: %v", err)
	}
	state.discardedID = dead.ID()
	if err := root.Discard(dead); err != nil {
		t.Fatalf("discard child: %v", err)
	}

	// An isolated worktree child with a sealed output — the crash
	// happened mid-fork, before the settlement.
	wtPath := filepath.Join(t.TempDir(), "wt-a")
	sb := shepherdgit.NewWorktreeSandbox(repo, wtPath)
	if err := sb.Create(context.Background(), shepherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	wtChild, err := mgr.ForkIsolated(root.ID(), "sub-worktree", nil, sb)
	if err != nil {
		t.Fatalf("fork isolated child: %v", err)
	}
	state.worktreeChildID = wtChild.ID()
	state.worktreePath = wtPath
	if err := os.WriteFile(filepath.Join(wtPath, "variant.txt"), []byte("speculative\n"), 0o644); err != nil {
		t.Fatalf("write worktree content: %v", err)
	}
	out, err := mgr.Seal(context.Background(), wtChild.ID())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	state.outputID = out.ID()

	// The crash: the process dies without closing anything.
	return state
}

// reopen simulates the restart: a fresh process opens the same
// durable trace and recovers.
func reopen(t *testing.T, state *crashState) (*shepherd.ScopeManager, *Report) {
	t.Helper()
	store, err := shepherdtrace.NewSQLiteTraceStore(state.storePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	mgr, report, err := Recover(context.Background(), store, state.repo, true, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	return mgr, report
}

// TestRecover_RebuildsTreeTerminalsCheckpointsOutputs pins the R1
// core: a crash-simulated restart rebuilds the scope tree (terminal
// states included), the checkpoint registry, and the sealed outputs
// from the durable trace, and re-adopts the live worktree child.
func TestRecover_RebuildsTreeTerminalsCheckpointsOutputs(t *testing.T) {
	state := buildCrashState(t)

	mgr, report := reopen(t, state)

	// The tree: root active, shared child attached to it, the
	// discarded child terminal, the worktree child active with its
	// worktree re-adopted.
	root, ok := mgr.Get("scope:sess-old")
	if !ok || root.State() != shepherd.ScopeActive {
		t.Fatalf("root not recovered active: ok=%v state=%v", ok, root.State())
	}
	// Roots resolve with an empty backend by the kernel's recovery
	// contract; the resolver must re-adopt the in-place git repository
	// on them (review finding: this was unverified).
	if root.Sandbox() == nil {
		t.Error("the root must be re-adopted onto the in-place git repository")
	}
	shared, ok := mgr.Get(state.sharedChildID)
	if !ok || shared.State() != shepherd.ScopeActive || shared.Parent() == nil || shared.Parent().ID() != root.ID() {
		t.Fatalf("shared child not recovered under the root: ok=%v state=%s", ok, shared.State())
	}
	dead, ok := mgr.Get(state.discardedID)
	if !ok || dead.State() != shepherd.ScopeDiscarded {
		t.Fatalf("terminal state not recovered: ok=%v state=%v", ok, dead.State())
	}
	wtChild, ok := mgr.Get(state.worktreeChildID)
	if !ok || wtChild.State() != shepherd.ScopeActive {
		t.Fatalf("worktree child not recovered: ok=%v state=%v", ok, wtChild.State())
	}
	if wtChild.Sandbox() == nil {
		t.Error("the live worktree child must be re-adopted (its unmerged work is settle-able again)")
	}
	if len(report.Adopted) != 1 || report.Adopted[0] != state.worktreePath {
		t.Errorf("report.Adopted = %v, want the live worktree's path", report.Adopted)
	}
	if report.Scopes != 4 || report.Roots != 1 {
		t.Errorf("report = %d scopes, %d roots; want 4, 1", report.Scopes, report.Roots)
	}

	// The checkpoint registry: the recorded checkpoint restores.
	snap, err := mgr.RestoreCheckpoint(context.Background(), state.checkpointID)
	if err != nil {
		t.Fatalf("recovered checkpoint must restore: %v", err)
	}
	if !strings.Contains(string(snap), "unit one") {
		t.Errorf("restored snapshot = %q, want the recorded conversation", snap)
	}

	// The retained outputs: the sealed output is settle-able again.
	out, ok := mgr.Output(state.outputID)
	if !ok || out.State() != shepherd.OutputUnconsumed {
		t.Fatalf("sealed output not recovered settle-able: ok=%v state=%v", ok, out.State())
	}
	if len(out.Changes()) == 0 {
		t.Error("the recovered output must carry its frozen change list")
	}
}

// TestRecover_StaleCheckpointRefusedWithReason pins the staleness
// contract (R1/D5): a checkpoint whose recorded revision no longer
// matches the re-adopted workspace is marked CheckpointInvalid by
// recovery and refused with the recorded reason, never silently
// restored onto a tree that moved past it.
func TestRecover_StaleCheckpointRefusedWithReason(t *testing.T) {
	state := buildCrashState(t)

	// The crash is followed by other work: the repository's HEAD moves.
	if err := os.WriteFile(filepath.Join(state.repo, "moved-on.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, state.repo, "add", "-A")
	mustGit(t, state.repo, "commit", "-m", "moved on")

	mgr, _ := reopen(t, state)

	_, err := mgr.RestoreCheckpoint(context.Background(), state.checkpointID)
	if err == nil || !strings.Contains(err.Error(), "invalid, cannot restore") {
		t.Fatalf("stale checkpoint must refuse with the recorded reason, got: %v", err)
	}
}

// TestRecover_FailsLoudlyOnCorruptTrace pins the pass-through (R1): a
// recovery that cannot trust the trace fails loudly instead of
// handing back a half-rebuilt registry. The corruption is a fork
// record without a child owner — the kernel's scan refuses it.
func TestRecover_FailsLoudlyOnCorruptTrace(t *testing.T) {
	repo := newGitRepo(t)
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	// A malformed fork record: no child_owner, which the kernel's
	// recovery scan treats as a corrupt trace.
	if _, err := store.Append(context.Background(), shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: "sess-bad:fork:corrupt:1",
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID: "sess-bad",
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Declaration,
				SchemaRef: shepherd.SchemaScopeForked,
				KindLabel: "scope:forked",
				Payload:   map[string]any{"isolated": false},
			}},
		}},
	}); err != nil {
		t.Fatalf("append corrupt record: %v", err)
	}

	if _, _, err := Recover(context.Background(), store, repo, true, nil); err == nil {
		t.Fatal("a corrupt trace must fail the recovery loudly")
	}
}

// TestRecover_OrphanListedAndLeftOnDisk pins the warn-only orphan
// contract (R1/D5): a worktree whose scope is terminal but whose files
// still stand — the crash window between the discard record and the
// teardown — is listed with its scope, path, and size, and survives
// recovery byte-identical. Nothing is deleted.
func TestRecover_OrphanListedAndLeftOnDisk(t *testing.T) {
	state := buildCrashState(t)

	// The crash window: the discard was recorded, the teardown never
	// ran. First recover (the scopes live in the manager's memory, so
	// the discard must run through a recovered manager), discard the
	// worktree child through the kernel — which destroys its worktree
	// and records the terminal state — then restore the directory
	// with sentinel content: the exact bytes an unmerged orphan would
	// hold.
	mgrA, _ := reopen(t, state)
	root, ok := mgrA.Get("scope:sess-old")
	if !ok {
		t.Fatal("root vanished after recovery")
	}
	child, ok := mgrA.Get(state.worktreeChildID)
	if !ok {
		t.Fatal("worktree child vanished after recovery")
	}
	if err := root.Discard(child); err != nil {
		t.Fatalf("discard child: %v", err)
	}
	if err := os.MkdirAll(state.worktreePath, 0o755); err != nil {
		t.Fatalf("restore orphan dir: %v", err)
	}
	sentinel := filepath.Join(state.worktreePath, "unmerged.txt")
	if err := os.WriteFile(sentinel, []byte("the only copy\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// The restart after the crash: recovery classifies the orphan.
	mgr, report := reopen(t, state)

	// The scope is discarded and the worktree is listed as an orphan.
	var found *Orphan
	for i, o := range report.Orphans {
		if o.ScopeID == state.worktreeChildID {
			found = &report.Orphans[i]
		}
	}
	if found == nil {
		t.Fatalf("the orphaned worktree must be listed (report: %+v)", report)
	}
	if found.Path != state.worktreePath || found.Size <= 0 {
		t.Errorf("orphan = %+v, want the path and a nonzero size", found)
	}
	if scope, ok := mgr.Get(state.worktreeChildID); !ok || scope.State() != shepherd.ScopeDiscarded {
		t.Errorf("the discarded scope must recover terminal (ok=%v)", ok)
	} else if scope.Sandbox() != nil {
		t.Error("an orphan must stay inert: no owned sandbox handle a later teardown could destroy it through")
	}

	// Warn-only means byte-identical survival: the sentinel content is
	// untouched, and the directory still stands after recovery.
	data, rerr := os.ReadFile(sentinel)
	if rerr != nil || string(data) != "the only copy\n" {
		t.Errorf("the orphan must survive recovery byte-identical, got %q (%v)", data, rerr)
	}
	if _, serr := os.Stat(state.worktreePath); serr != nil {
		t.Errorf("the orphaned worktree directory must be left on disk: %v", serr)
	}
}

// TestRecover_MissingWorktreeReported pins the honest loss report
// (R1/D5): an active worktree child whose recorded path is gone lost
// its unmerged work in the crash — reported with scope and path. A
// terminal child whose worktree is gone is the expected post-teardown
// state and stays silent.
func TestRecover_MissingWorktreeReported(t *testing.T) {
	state := buildCrashState(t)

	// The crash lost the worktree: the path is recorded, the directory
	// is gone.
	if err := os.RemoveAll(state.worktreePath); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	mgr, report := reopen(t, state)

	if len(report.Missing) != 1 || report.Missing[0].ScopeID != state.worktreeChildID || report.Missing[0].Path != state.worktreePath {
		t.Fatalf("the missing worktree must be reported (missing: %+v)", report.Missing)
	}
	if len(report.Orphans) != 0 {
		t.Errorf("a gone worktree is not an orphan (orphans: %+v)", report.Orphans)
	}
	if scope, ok := mgr.Get(state.worktreeChildID); !ok || scope.Sandbox() != nil {
		t.Errorf("the child must recover without a sandbox (ok=%v)", ok)
	}
}

// TestRecover_SquatterDirectoryNeverAdopted pins the strict adoption
// rule (review finding): the trace directory is shared across runs and
// an unrelated directory can occupy a recorded path — adopting it as an
// owned worktree sandbox would hand a later teardown a RemoveAll over
// user data. The squatter is left untouched and unadopted.
func TestRecover_SquatterDirectoryNeverAdopted(t *testing.T) {
	repo := newGitRepo(t)
	dir := t.TempDir()
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(dir, "trace.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	// An unregistered plain directory squats on the recorded path.
	squat := filepath.Join(t.TempDir(), "squatter")
	if err := os.MkdirAll(squat, 0o755); err != nil {
		t.Fatalf("mkdir squatter: %v", err)
	}
	if err := os.WriteFile(filepath.Join(squat, "user-data.txt"), []byte("not yaah's\n"), 0o644); err != nil {
		t.Fatalf("write user data: %v", err)
	}

	mgr1 := shepherd.NewScopeManager(store)
	root, err := mgr1.Create("sess-squat", shepherdgit.NewLocalGitSandbox(repo))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	// The isolated fork never provisioned a real worktree — the crash
	// left the scope pointing at the squatter's path.
	child, err := mgr1.ForkIsolated(root.ID(), "sub-squat", nil, shepherdgit.NewWorktreeSandbox(repo, squat))
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	store.Close(context.Background())

	store2, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(dir, "trace.sqlite"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { store2.Close(context.Background()) })
	mgr, report, err := Recover(context.Background(), store2, repo, true, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	scope, ok := mgr.Get(child.ID())
	if !ok || scope.State() != shepherd.ScopeActive {
		t.Fatalf("child not recovered active (ok=%v)", ok)
	}
	if scope.Sandbox() != nil {
		t.Error("a squatter directory must never be adopted as an owned sandbox")
	}
	if len(report.Adopted) != 0 {
		t.Errorf("report.Adopted = %v, want none (nothing adoptable)", report.Adopted)
	}
	// The refusal is reported, not silenced: an active child whose
	// recorded occupant is unadoptable lands in the unadopted bucket.
	if len(report.Unadopted) != 1 || report.Unadopted[0].ScopeID != child.ID() || report.Unadopted[0].Path != squat {
		t.Errorf("the unadopted squatter must be reported (unadopted: %+v)", report.Unadopted)
	}
	// Untouched: recovery never deletes what it does not own.
	data, rerr := os.ReadFile(filepath.Join(squat, "user-data.txt"))
	if rerr != nil || string(data) != "not yaah's\n" {
		t.Errorf("the squatter's data must survive untouched, got %q (%v)", data, rerr)
	}
}

// TestRecover_IsolatedSessionKeepsRootsCausal pins the D2 rule on
// restart (review finding): an isolated session's root is deliberately
// pure-causal — the container is the substrate — and recovery must not
// attach a host git sandbox where creation refused one. The kernel
// passes the same empty backend for every root, so the host session's
// mode is the discriminator.
func TestRecover_IsolatedSessionKeepsRootsCausal(t *testing.T) {
	state := buildCrashState(t)

	mgr, _ := reopenMode(t, state, false)

	root, ok := mgr.Get("scope:sess-old")
	if !ok || root.State() != shepherd.ScopeActive {
		t.Fatalf("root not recovered active (ok=%v)", ok)
	}
	if root.Sandbox() != nil {
		t.Error("an isolated session's root must recover pure-causal — no host git sandbox (D2)")
	}
}

// reopenMode recovers with an explicit adoptRoots mode.
func reopenMode(t *testing.T, state *crashState, adoptRoots bool) (*shepherd.ScopeManager, *Report) {
	t.Helper()
	store, err := shepherdtrace.NewSQLiteTraceStore(state.storePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	mgr, report, err := Recover(context.Background(), store, state.repo, adoptRoots, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	return mgr, report
}

// appendLegacyWorktreeRecord writes a pre-v0.14 yaah.worktree.provisioned.v1
// record — the shape traces written before the kernel recorded sandbox
// addresses itself carry. Tests use it to stand in for a fork made by an
// older yaah against the same kernel trace.
func appendLegacyWorktreeRecord(ctx context.Context, store *shepherdtrace.SQLiteTraceStore, owner, repoPath, worktreePath string) error {
	_, err := store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: fmt.Sprintf("%s:worktree:%d", owner, time.Now().UnixNano()),
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID: owner,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Declaration,
				SchemaRef: SchemaWorktreeProvisioned,
				KindLabel: "worktree:provisioned",
				Payload: map[string]any{
					"path": worktreePath,
					"repo": repoPath,
				},
			}},
		}},
	})
	if err != nil {
		return fmt.Errorf("record worktree path for %s: %w", owner, err)
	}
	return nil
}

// TestRecover_KernelRecordedWorktreeAdoptsWithoutYaahRecord is plan 06
// phase D's retirement law: a fork made after the kernel v0.14 bump writes
// no yaah.worktree.provisioned record — the kernel's own fork record
// carries the sandbox address — and recovery still re-adopts the
// worktree, with the pre-crash work live, and lists it as adopted.
func TestRecover_KernelRecordedWorktreeAdoptsWithoutYaahRecord(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	repo := newGitRepo(t)
	ctx := context.Background()

	mgr := shepherd.NewScopeManager(store)
	root, err := mgr.Create("sess-new", shepherdgit.NewLocalGitSandbox(repo))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}

	wtPath := filepath.Join(t.TempDir(), "wt-new")
	sb := shepherdgit.NewWorktreeSandbox(repo, wtPath)
	if err := sb.Create(ctx, shepherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	child, err := mgr.ForkIsolated(root.ID(), "sub-new", nil, sb)
	if err != nil {
		t.Fatalf("fork isolated: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "live.txt"), []byte("pre-crash\n"), 0o644); err != nil {
		t.Fatalf("write worktree content: %v", err)
	}

	// The crash: nothing is closed, no yaah record is written, and the
	// restarted process knows only the trace.
	reopened, err := shepherdtrace.NewSQLiteTraceStore(storePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { reopened.Close(context.Background()) })
	rmgr, report, err := Recover(ctx, reopened, repo, true, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	rc, ok := rmgr.Get(child.ID())
	if !ok {
		t.Fatal("worktree child not recovered")
	}
	if rc.Sandbox() == nil {
		t.Fatal("the kernel-recorded worktree was not re-adopted; recovery must find it from the fork record alone")
	}
	found := false
	for _, p := range report.Adopted {
		if p == wtPath {
			found = true
		}
	}
	if !found {
		t.Errorf("the re-adopted worktree is not on the adopted report: %v", report.Adopted)
	}
	if data, rerr := os.ReadFile(filepath.Join(wtPath, "live.txt")); rerr != nil || string(data) != "pre-crash\n" {
		t.Errorf("the pre-crash work is not live through the re-adopted worktree: %q, %v", string(data), rerr)
	}
}

// TestRecover_LegacyRecordFallbackAdopts exercises the pre-bump trace
// shape genuinely: a fork record written before the kernel carried
// sandbox addresses (no sandbox_path in the payload) alongside yaah's own
// worktree record. Recovery must adopt from the yaah record — the
// fallback deleted at the next minor — with the same strict verification.
func TestRecover_LegacyRecordFallbackAdopts(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	repo := newGitRepo(t)
	ctx := context.Background()

	mgr := shepherd.NewScopeManager(store)
	root, err := mgr.Create("sess-legacy", shepherdgit.NewLocalGitSandbox(repo))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}

	wtPath := filepath.Join(t.TempDir(), "wt-legacy")
	sb := shepherdgit.NewWorktreeSandbox(repo, wtPath)
	if err := sb.Create(ctx, shepherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "old.txt"), []byte("pre-bump work\n"), 0o644); err != nil {
		t.Fatalf("write worktree content: %v", err)
	}

	// The pre-bump trace shape: the fork record carries the backend but
	// no sandbox address, and yaah's own record holds the path.
	if _, err := store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: "sess-legacy:fork-legacy",
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID: root.OwnerID(),
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Declaration,
				SchemaRef: shepherd.SchemaScopeForked,
				KindLabel: "scope:forked",
				Payload: map[string]any{
					"child_owner":     "sub-legacy",
					"isolated":        true,
					"sandbox_backend": "git",
				},
			}},
		}},
	}); err != nil {
		t.Fatalf("append pre-bump fork record: %v", err)
	}
	if err := appendLegacyWorktreeRecord(ctx, store, "sub-legacy", repo, wtPath); err != nil {
		t.Fatalf("append legacy worktree record: %v", err)
	}
	store.Close(context.Background())

	// The crash and restart.
	reopened, err := shepherdtrace.NewSQLiteTraceStore(storePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { reopened.Close(context.Background()) })
	rmgr, report, err := Recover(ctx, reopened, repo, true, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	rc, ok := rmgr.Get("scope:sub-legacy")
	if !ok {
		t.Fatal("legacy child not recovered")
	}
	if rc.Sandbox() == nil {
		t.Fatal("the legacy record's worktree was not adopted; the pre-bump fallback must resolve it")
	}
	found := false
	for _, p := range report.Adopted {
		if p == wtPath {
			found = true
		}
	}
	if !found {
		t.Errorf("the adopted legacy worktree is not on the report: %v", report.Adopted)
	}
	if data, rerr := os.ReadFile(filepath.Join(wtPath, "old.txt")); rerr != nil || string(data) != "pre-bump work\n" {
		t.Errorf("the pre-bump work is not live through the adopted worktree: %q, %v", string(data), rerr)
	}
}
