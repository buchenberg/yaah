package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	goatherdgit "github.com/buchenberg/goatherd-runtime/sandbox/git"
	"github.com/buchenberg/yaah/internal/config"
)

// TestWorktreeSandboxOutlivesRunEndDiscard pins the yaah-95ri.6 fix: the
// run-end Discard of a body scope must never destroy its worktree. The
// worktree outlives the run for recovery (the fork record's sandbox
// address is the recovery mechanism) and, on the retained lane, for the
// proposal's seal; turn checkpoints read the sandbox off the scope handle
// during the run. The scope therefore attaches its worktree sandbox
// NON-owned (WithSandbox(sb, false)) and run-end Discard is a pure
// scope-state completion.
func TestWorktreeSandboxOutlivesRunEndDiscard(t *testing.T) {
	mgr, rootID := scopeCapFixture(t)
	repo := newRunnerTestGitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")

	sb := goatherdgit.NewWorktreeSandbox(repo, wt)
	if err := sb.Create(context.Background(), goatherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("create worktree sandbox: %v", err)
	}

	opts := taskRunnerOpts{
		rootScopeID:       rootID,
		scopeManager:      mgr,
		maxDispatchScopes: 8,
		defaults:          config.Defaults{RetainedRepoPath: repo},
	}
	child, err := createSubAgentScope(mgr, opts, "wt-trace", wt)
	if err != nil {
		t.Fatalf("isolated fork: %v", err)
	}
	if child.Sandbox() == nil {
		t.Fatal("turn checkpoints need the sandbox reachable from the scope handle")
	}

	// Run end: the body's deferred Discard. The worktree must survive it.
	if err := mgr.Discard(child.ID()); err != nil {
		t.Fatalf("run-end discard: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("run-end discard destroyed the worktree (proposal loss / recovery loss): %v", err)
	}
}
