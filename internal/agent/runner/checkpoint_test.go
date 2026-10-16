package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	shepherd "github.com/buchenberg/shepherd-runtime-go"
	"github.com/buchenberg/yaah/internal/gitsandbox"
)

// TestShepherdTurnCheckpointer_RestoreRevertsWorkspace verifies the adapter's
// two-phase behavior: Checkpoint captures the workspace plus a snapshot, and
// Restore rewinds both — returning the stored snapshot and reverting file
// changes. It also pins the single-use contract (a second restore must fail).
func TestShepherdTurnCheckpointer_RestoreRevertsWorkspace(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	mgr := shepherd.NewScopeManager(store)

	repo := newRunnerTestGitRepo(t)
	sb := gitsandbox.NewLocalSandbox(repo)
	scope, err := mgr.Create("turn-checkpoint-test", sb)
	if err != nil {
		t.Fatalf("create scope: %v", err)
	}

	ck := NewShepherdTurnCheckpointer(mgr, scope.ID(), sb)

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
	// Production restores run through the untracked-preserving sandbox
	// (gitsandbox): captured content is rewound, but files created after
	// the checkpoint deliberately survive (#313).
	if got, err := os.ReadFile(filepath.Join(repo, "untracked.txt")); err != nil || string(got) != "junk" {
		t.Errorf("uncaptured untracked file should survive restore, got %q err %v", got, err)
	}

	// Single-use: the checkpoint was consumed, so restoring it again fails.
	if _, err := ck.Restore(ctx, id); err == nil {
		t.Error("second Restore should fail (checkpoints are single-use)")
	}
}

// newRunnerTestGitRepo creates a temp git repo with an initial commit,
// isolated from the developer's global/system git config. Accepts
// testing.TB so both tests and benchmarks can use it.
func newRunnerTestGitRepo(tb testing.TB) string {
	tb.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		tb.Skip("git binary not available")
	}
	dir := tb.TempDir()
	tb.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(tb.TempDir(), "gitconfig"))
	tb.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(tb.TempDir(), "gitconfig-system"))
	mustRunnerGit(tb, dir, "init")
	mustRunnerGit(tb, dir, "config", "user.email", "test@test.com")
	mustRunnerGit(tb, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test"), 0o644); err != nil {
		tb.Fatalf("write README: %v", err)
	}
	mustRunnerGit(tb, dir, "add", "-A")
	mustRunnerGit(tb, dir, "commit", "-m", "init")
	return dir
}

func mustRunnerGit(tb testing.TB, dir string, args ...string) {
	tb.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		tb.Fatalf("git %s in %s: %v\n%s", args[0], dir, err, out)
	}
}
