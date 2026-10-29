// Command orch-seed prepares the orch-recovery fixture at image build time.
//
// It creates a normal git root plus an isolated worktree child, seals the
// child, and leaves the trace store on disk. The process then exits without
// settling anything: that is the simulated crash the task's recovery path
// must rebuild from.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	goatherdgit "github.com/buchenberg/goatherd-runtime/sandbox/git"
)

func main() {
	repo := flag.String("repo", "/app", "repository root")
	tracePath := flag.String("trace", "/app/.orch-trace/trace.sqlite", "trace database path")
	worktree := flag.String("worktree", "/app/.orch-worktree/wt", "isolated worktree path")
	flag.Parse()

	ctx := context.Background()
	if err := os.MkdirAll(filepath.Dir(*tracePath), 0o755); err != nil {
		fail("trace dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*worktree), 0o755); err != nil {
		fail("worktree parent: %v", err)
	}
	store, err := shepherdtrace.NewSQLiteTraceStore(*tracePath)
	if err != nil {
		fail("open trace: %v", err)
	}
	defer store.Close(ctx)

	mgr := goatherd.NewScopeManager(store)
	root, err := mgr.Create("sess-recovery", goatherdgit.NewLocalGitSandbox(*repo))
	if err != nil {
		fail("create root: %v", err)
	}

	sb := goatherdgit.NewWorktreeSandbox(*repo, *worktree)
	if err := sb.Create(ctx, goatherdsandbox.SandboxSpec{}); err != nil {
		fail("create worktree: %v", err)
	}
	child, err := mgr.ForkIsolated(root.ID(), "sub-recovery", nil, sb)
	if err != nil {
		fail("fork isolated child: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*worktree, "recovered.txt"), []byte("survived the crash\n"), 0o644); err != nil {
		fail("write worktree payload: %v", err)
	}
	out, err := mgr.Seal(ctx, child.ID())
	if err != nil {
		fail("seal child: %v", err)
	}
	fmt.Printf("scope=%s worktree=%s output=%s\n", child.ID(), *worktree, out.ID())
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "orch-seed: "+format+"\n", args...)
	os.Exit(1)
}
