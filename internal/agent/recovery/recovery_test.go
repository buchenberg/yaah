package recovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"
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
	root, err := mgr.Create("sess-old", shepherdsandbox.NewLocalGitSandbox(repo))
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
	sb := shepherdsandbox.NewWorktreeSandbox(repo, wtPath)
	if err := sb.Create(context.Background(), shepherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	wtChild, err := mgr.ForkIsolated(root.ID(), "sub-worktree", nil, sb)
	if err != nil {
		t.Fatalf("fork isolated child: %v", err)
	}
	state.worktreeChildID = wtChild.ID()
	state.worktreePath = wtPath
	if err := RecordWorktree(context.Background(), store, wtChild.OwnerID(), repo, wtPath); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
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
	mgr, report, err := Recover(context.Background(), store, state.repo)
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
	if report.Adopted != 1 {
		t.Errorf("report.Adopted = %d, want 1 (the live worktree)", report.Adopted)
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

	if _, _, err := Recover(context.Background(), store, repo); err == nil {
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
	root, err := mgr1.Create("sess-squat", shepherdsandbox.NewLocalGitSandbox(repo))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	// The isolated fork never provisioned a real worktree — the crash
	// left the scope pointing at the squatter's path.
	child, err := mgr1.ForkIsolated(root.ID(), "sub-squat", nil, shepherdsandbox.NewWorktreeSandbox(repo, squat))
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if err := RecordWorktree(context.Background(), store, child.OwnerID(), repo, squat); err != nil {
		t.Fatalf("record: %v", err)
	}
	store.Close(context.Background())

	store2, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(dir, "trace.sqlite"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { store2.Close(context.Background()) })
	mgr, report, err := Recover(context.Background(), store2, repo)
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
	if report.Adopted != 0 {
		t.Errorf("report.Adopted = %d, want 0 (nothing adoptable)", report.Adopted)
	}
	// Untouched: recovery never deletes what it does not own.
	data, rerr := os.ReadFile(filepath.Join(squat, "user-data.txt"))
	if rerr != nil || string(data) != "not yaah's\n" {
		t.Errorf("the squatter's data must survive untouched, got %q (%v)", data, rerr)
	}
}
