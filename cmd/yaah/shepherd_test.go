package yaah

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

	// The orphan: a real worktree, then a discard through the kernel
	// (teardown happens) with the directory restored by hand — the
	// crash window's exact bytes.
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
	if err := os.MkdirAll(orphanWT, 0o755); err != nil {
		t.Fatalf("restore orphan dir: %v", err)
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
	if !strings.Contains(listed, "yaah shepherd prune "+orphanWT) {
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
	if err := pruneOrphan(report, orphanWT+"/x"); err == nil {
		t.Error("a path that is not exactly on the report must refuse")
	}
	if _, err := os.Stat(orphanWT); err != nil {
		t.Fatalf("a refused prune must not delete anything: %v", err)
	}

	// The adopted (live) worktree refuses explicitly — it is unmerged
	// work in use.
	err := pruneOrphan(report, liveWT)
	if err == nil || !strings.Contains(err.Error(), "adopted (live)") {
		t.Errorf("pruning an adopted worktree must refuse explicitly, got: %v", err)
	}
	if _, serr := os.Stat(liveWT); serr != nil {
		t.Fatalf("the adopted worktree must be untouched: %v", serr)
	}

	// The exact name prunes: the sentinel goes, the live worktree
	// stays.
	if err := pruneOrphan(report, orphanWT); err != nil {
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
	err := pruneOrphan(nil, "/tmp/whatever")
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
