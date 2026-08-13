package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// TestShepherdTurnCheckpointer_RestoreRevertsWorkspace verifies the adapter's
// two-phase behavior: Checkpoint captures the workspace plus a snapshot, and
// Restore rewinds both — returning the stored snapshot and reverting file
// changes. It also pins the single-use contract (a second restore must fail).
func TestShepherdTurnCheckpointer_RestoreRevertsWorkspace(t *testing.T) {
	store, err := shepherd.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	mgr := shepherd.NewScopeManager(store)
	scope, err := mgr.Create("turn-checkpoint-test")
	if err != nil {
		t.Fatalf("create scope: %v", err)
	}

	repo := newRunnerTestGitRepo(t)
	ck := NewShepherdTurnCheckpointer(mgr, scope.ID(), repo)

	ctx := context.Background()

	// Commit a tracked file so we can observe a tracked modification revert.
	base := filepath.Join(repo, "base.txt")
	if err := os.WriteFile(base, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	mustRunnerGit(t, repo, "add", "base.txt")
	mustRunnerGit(t, repo, "commit", "-m", "base")

	id, err := ck.Checkpoint(ctx, []byte("snap-1"))
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if id == "" {
		t.Fatal("Checkpoint returned empty id")
	}

	// Mutate the workspace: modify a tracked file and add an untracked file.
	if err := os.WriteFile(base, []byte("v2"), 0o644); err != nil {
		t.Fatalf("modify base: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	snapshot, err := ck.Restore(ctx, id)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if string(snapshot) != "snap-1" {
		t.Errorf("snapshot = %q, want %q", snapshot, "snap-1")
	}

	got, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read restored base: %v", err)
	}
	if string(got) != "v1" {
		t.Errorf("base after restore = %q, want %q", got, "v1")
	}
	if _, err := os.Stat(filepath.Join(repo, "untracked.txt")); !os.IsNotExist(err) {
		t.Errorf("untracked file should be gone after restore, stat err = %v", err)
	}

	// Single-use: the checkpoint was consumed, so restoring it again fails.
	if _, err := ck.Restore(ctx, id); err == nil {
		t.Error("second Restore should fail (checkpoints are single-use)")
	}
}

func newRunnerTestGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "gitconfig-system"))
	mustRunnerGit(t, dir, "init")
	mustRunnerGit(t, dir, "config", "user.email", "test@test.com")
	mustRunnerGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustRunnerGit(t, dir, "add", "-A")
	mustRunnerGit(t, dir, "commit", "-m", "init")
	return dir
}

func mustRunnerGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", args[0], dir, err, out)
	}
}
