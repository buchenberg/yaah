package supervision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	goatherdgit "github.com/buchenberg/goatherd-runtime/sandbox/git"

	"github.com/buchenberg/yaah/internal/agent/recovery"
)

// mustGitRepo builds a committed repository — worktrees and captures both
// need a HEAD. Local to this file so the recovery package's own fixture
// stays theirs.
func mustGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "gitconfig-system"))
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %v\n%s", args[0], dir, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "-A")
	run("commit", "-m", "init")
	return dir
}

// TestReaperStartKeepsReadoptedWorktree is the orch-recovery scenario
// behind yaah-dbik, end to end at the seam where it broke: a seeded trace
// holds an ACTIVE worktree child whose output was sealed but never
// settled (the crash), recovery re-admits the worktree ("its work is
// live again"), and the reaper's startup sweep — the pass that retired
// scope:sub-recovery in the failing run — must leave that worktree
// standing, registered, and readable. The seal is the kernel's own, with
// no yaah proposal metadata, so the pending-source exemption has nothing
// to spare: only the re-adopted substrate itself may hold the line.
func TestReaperStartKeepsReadoptedWorktree(t *testing.T) {
	ctx := context.Background()
	repo := mustGitRepo(t)

	storePath := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// The seed: root + isolated worktree child, payload written, output
	// sealed, nothing settled, no teardown — the simulated crash.
	mgr := goatherd.NewScopeManager(store)
	root, err := mgr.Create("sess-recovery", goatherdgit.NewLocalGitSandbox(repo))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	wtPath := filepath.Join(t.TempDir(), "wt")
	sb := goatherdgit.NewWorktreeSandbox(repo, wtPath)
	if err := sb.Create(ctx, goatherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	child, err := mgr.ForkIsolated(root.ID(), "sub-recovery", nil, sb)
	if err != nil {
		t.Fatalf("fork isolated: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "recovered.txt"), []byte("survived the crash\n"), 0o644); err != nil {
		t.Fatalf("write worktree payload: %v", err)
	}
	if _, err := mgr.Seal(ctx, child.ID()); err != nil {
		t.Fatalf("seal child: %v", err)
	}
	store.Close(ctx)

	// The restart: recovery rebuilds the tree and re-adopts the worktree.
	reopened, err := shepherdtrace.NewSQLiteTraceStore(storePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { reopened.Close(ctx) })
	rmgr, report, err := recovery.Recover(ctx, reopened, repo, true, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(report.Adopted) != 1 || report.Adopted[0] != wtPath {
		t.Fatalf("recovery adopted %v, want [%s] — the scenario presumes the re-adoption", report.Adopted, wtPath)
	}

	// The wiring's handover: every recovered ACTIVE scope is resumed, the
	// reaper is told so, and Start runs the startup sweep before anything
	// else can. The session's own root is a NEW scope (a restart mints
	// one), so nothing but the exemptions below can spare the recovered
	// pair.
	r := NewReaper(rmgr, NewRegistry(), "scope:session-new", 0)
	t.Cleanup(r.Close)
	ids := make([]string, 0, len(report.Resumed))
	for _, entry := range report.Resumed {
		ids = append(ids, entry.ScopeID)
	}
	r.SetResumed(ids)
	r.Start()

	if _, ok := rmgr.Get(child.ID()); !ok {
		t.Fatal("the startup sweep retired the re-adopted worktree child")
	}
	data, err := os.ReadFile(filepath.Join(wtPath, "recovered.txt"))
	if err != nil || string(data) != "survived the crash\n" {
		t.Fatalf("the re-adopted worktree's work did not survive the startup sweep: %q, %v", string(data), err)
	}
	if !recovery.IsRegisteredWorktree(ctx, repo, wtPath) {
		t.Fatalf("%s is no longer a registered worktree of %s", wtPath, repo)
	}
}
