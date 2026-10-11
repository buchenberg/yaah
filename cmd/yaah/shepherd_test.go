package yaah

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"

	"github.com/buchenberg/yaah/internal/agent/recovery"
)

// shepherdOrphanEnv builds the crash-window state the prune tests
// need: one orphaned worktree (a discarded scope whose directory was
// restored by hand — the crash window between the discard record and
// the teardown) and one adopted (live) worktree, both recorded and
// recovered. It returns the recovery report.
func shepherdOrphanEnv(t *testing.T) (*recovery.Report, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	repo := newCmdGitRepo(t)
	tracePath := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mgr := shepherd.NewScopeManager(store)
	root, err := mgr.Create("sess-shep", shepherdsandbox.NewLocalGitSandbox(repo))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}

	// The live worktree: forked isolated, recorded, still active.
	liveWT := filepath.Join(t.TempDir(), "wt-live")
	liveSB := shepherdsandbox.NewWorktreeSandbox(repo, liveWT)
	if err := liveSB.Create(context.Background(), shepherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("create live worktree: %v", err)
	}
	liveChild, err := mgr.ForkIsolated(root.ID(), "sub-live", nil, liveSB)
	if err != nil {
		t.Fatalf("fork live: %v", err)
	}
	if err := recovery.RecordWorktree(context.Background(), store, liveChild.OwnerID(), repo, liveWT); err != nil {
		t.Fatalf("record live: %v", err)
	}

	// The orphan: a real worktree whose scope is terminal but whose
	// teardown never ran — the crash window between the discard record
	// and the sandbox destroy. Simulated by discarding through the
	// kernel (which destroys the worktree and records the terminal
	// state) and then re-adding a REAL worktree at the same path with
	// sentinel content: the exact shape the crash leaves.
	orphanWT := filepath.Join(t.TempDir(), "wt-dead")
	deadSB := shepherdsandbox.NewWorktreeSandbox(repo, orphanWT)
	if err := deadSB.Create(context.Background(), shepherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("create dead worktree: %v", err)
	}
	deadChild, err := mgr.ForkIsolated(root.ID(), "sub-dead", nil, deadSB)
	if err != nil {
		t.Fatalf("fork dead: %v", err)
	}
	if err := recovery.RecordWorktree(context.Background(), store, deadChild.OwnerID(), repo, orphanWT); err != nil {
		t.Fatalf("record dead: %v", err)
	}
	if err := root.Discard(deadChild); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "--detach", orphanWT).CombinedOutput(); err != nil {
		t.Fatalf("re-add worktree: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(orphanWT, "unmerged.txt"), []byte("the only copy\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	store.Close(context.Background())

	store2, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { store2.Close(context.Background()) })
	_, report, err := recovery.Recover(context.Background(), store2, repo, true)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	return report, orphanWT, liveWT
}

// TestShepherd_ListMatchesOrphanReport pins the R3 list: the command
// reports the recovery's own orphan classification — the orphaned
// worktree is listed with its scope and prunable hint, the live one
// as adopted.
func TestShepherd_ListMatchesOrphanReport(t *testing.T) {
	report, orphanWT, liveWT := shepherdOrphanEnv(t)

	var out strings.Builder
	printShepherdReport(&out, report)
	listed := out.String()
	if !strings.Contains(listed, "orphaned "+orphanWT) {
		t.Errorf("the list must name the orphaned worktree:\n%s", listed)
	}
	if !strings.Contains(listed, "adopted   "+liveWT) {
		t.Errorf("the list must name the adopted worktree:\n%s", listed)
	}
	if !strings.Contains(listed, "yaah shepherd prune \""+orphanWT+"\"") {
		t.Errorf("the orphan line must carry the prune hint:\n%s", listed)
	}
}

// TestShepherd_PruneRequiresExactName pins the R3 contract: only an
// exact path from the orphan report prunes — a near miss refuses, and
// an adopted (live) worktree refuses explicitly.
func TestShepherd_PruneRequiresExactName(t *testing.T) {
	report, orphanWT, liveWT := shepherdOrphanEnv(t)

	// A near miss (the orphan path plus a suffix) refuses and deletes
	// nothing.
	if err := pruneOrphan(io.Discard, report, orphanWT+"/x"); err == nil {
		t.Error("a path that is not exactly on the report must refuse")
	}
	if _, err := os.Stat(orphanWT); err != nil {
		t.Fatalf("a refused prune must not delete anything: %v", err)
	}

	// The adopted (live) worktree refuses explicitly — it is unmerged
	// work in use.
	err := pruneOrphan(io.Discard, report, liveWT)
	if err == nil || !strings.Contains(err.Error(), "adopted (live)") {
		t.Errorf("pruning an adopted worktree must refuse explicitly, got: %v", err)
	}
	if _, serr := os.Stat(liveWT); serr != nil {
		t.Fatalf("the adopted worktree must be untouched: %v", serr)
	}

	// The exact name prunes: the sentinel goes, the live worktree
	// stays.
	if err := pruneOrphan(io.Discard, report, orphanWT); err != nil {
		t.Fatalf("prune the exact orphan path: %v", err)
	}
	if _, err := os.Stat(orphanWT); !os.IsNotExist(err) {
		t.Errorf("the orphaned worktree must be gone, stat err = %v", err)
	}
	if _, err := os.Stat(liveWT); err != nil {
		t.Errorf("the adopted worktree must survive the prune: %v", err)
	}
}

// TestShepherd_PruneNilReport pins the off-tracing refusal: nothing
// to prune, stated plainly.
func TestShepherd_PruneNilReport(t *testing.T) {
	err := pruneOrphan(io.Discard, nil, "/tmp/whatever")
	if err == nil || !strings.Contains(err.Error(), "nothing to prune") {
		t.Errorf("pruning with tracing off must refuse, got: %v", err)
	}
}

// newCmdGitRepo creates a committed repository.
func newCmdGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "gitconfig-system"))
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "test@test.com"}, {"config", "user.name", "Test"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", args[0], err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", args[0], err, out)
		}
	}
	return dir
}

// TestShepherd_PruneRefusesReplacedDirectory pins the ownership check
// (review finding): git's registry can keep stale entries and a
// directory can be replaced with unrelated data — a user-supplied
// exact path is not ownership verification, so a listed orphan whose
// occupant is no longer a worktree of its recorded repository refuses.
func TestShepherd_PruneRefusesReplacedDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	repo := newCmdGitRepo(t)

	// A plain directory squatting on the orphan's recorded path — the
	// registry does not list it, and no .git file points home.
	dir := filepath.Join(t.TempDir(), "replaced")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user-data.txt"), []byte("not yaah's\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	report := &recovery.Report{Orphans: []recovery.Orphan{{ScopeID: "scope:sub-dead", Path: dir, Repo: repo}}}

	err := pruneOrphan(io.Discard, report, dir)
	if err == nil || !strings.Contains(err.Error(), "no longer a worktree of its recorded repository") {
		t.Errorf("pruning a replaced directory must refuse, got: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "user-data.txt")); serr != nil {
		t.Fatalf("the replaced directory's data must be untouched: %v", serr)
	}
}

// TestShepherd_PruneRefusesBrokenRegistry pins the registry-failure
// refusal (review finding): a git lookup failure must not fall back
// to deleting the directory — the registry is unverifiable, so the
// deletion refuses.
func TestShepherd_PruneRefusesBrokenRegistry(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := filepath.Join(t.TempDir(), "wt-any")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Repo points at a non-repository: git's worktree list fails.
	report := &recovery.Report{Orphans: []recovery.Orphan{{ScopeID: "scope:sub-x", Path: dir, Repo: t.TempDir()}}}

	err := pruneOrphan(io.Discard, report, dir)
	if err == nil || !strings.Contains(err.Error(), "registry unavailable") {
		t.Errorf("a broken worktree registry must refuse the prune, got: %v", err)
	}
	if _, serr := os.Stat(dir); serr != nil {
		t.Fatalf("the directory must survive the refused prune: %v", serr)
	}
}
