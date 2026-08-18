package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// Phase 9 of .agents/plans/per-turn-checkpoint-restore: measure the cost
// of per-turn git checkpoints to decide whether per-turn checkpointing
// (per-role `turn_checkpoints`) can ever default on.
//
// Each Checkpoint runs three git subprocesses (add -A, stash create,
// rev-parse HEAD) plus one SQLite append, so the cost is dominated by
// process spawn + git's index scan and scales with repo size. Restore
// adds reset --hard + clean -fd (+ stash apply when dirty).
//
// Measured (Intel Core Ultra 7 265H, Windows, Go 1.25.8, -benchtime=10x):
//
//	Checkpoint clean /  50 files   ~241 ms/op
//	Checkpoint clean / 500 files   ~320 ms/op
//	Checkpoint dirty /  50 files   ~304 ms/op
//	Checkpoint dirty / 500 files   ~385 ms/op
//	Restore cycle    /  50 files   ~447 ms/op
//	Restore cycle    / 500 files  ~1542 ms/op
//
// The cost is dominated by spawning three git subprocesses per checkpoint
// (Windows process creation is expensive), not by git's work — the clean
// 50-file case is almost all startup. Restore is rarer (only on a failed
// turn) but 2-4x the checkpoint cost, and grows sharply with repo size.
//
// Decision: keep per-turn checkpointing OFF by default (per-role
// `turn_checkpoints` is false unless set). A 240-385 ms checkpoint per
// turn is a meaningful fraction of a fast turn, and the restore path can
// exceed a second on medium repos. It is fine as an opt-in for repos
// where correctness outweighs throughput; revisit if a lower-overhead
// checkpoint backend (e.g. libgit2 in-process, or a single `git stash`
// invocation) lands, or on Linux where git spawns are several times cheaper.

// benchRepo builds a git repo with n committed files (plus README),
// approximating a small and a medium project tree.
func benchRepo(b *testing.B, files int) string {
	b.Helper()
	repo := newRunnerTestGitRepo(b)
	for i := 0; i < files; i++ {
		name := filepath.Join(repo, fmt.Sprintf("pkg/file_%04d.go", i))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		content := fmt.Sprintf("package pkg\n\n// file %d\nvar V%d = %d\n", i, i, i)
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			b.Fatalf("write %s: %v", name, err)
		}
	}
	mustRunnerGit(b, repo, "add", "-A")
	mustRunnerGit(b, repo, "commit", "-m", "seed files")
	return repo
}

func benchCheckpointer(b *testing.B, repo string) *ShepherdTurnCheckpointer {
	b.Helper()
	store, err := shepherd.NewSQLiteTraceStore(":memory:")
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { store.Close() })
	mgr := shepherd.NewScopeManager(store)
	scope, err := mgr.Create("bench")
	if err != nil {
		b.Fatalf("create scope: %v", err)
	}
	return NewShepherdTurnCheckpointer(mgr, scope.ID(), repo)
}

// BenchmarkTurnCheckpoint_CleanTree measures the common case: the turn
// made no file changes, so `git stash create` returns empty and only the
// index scan + HEAD lookup run.
func BenchmarkTurnCheckpoint_CleanTree(b *testing.B) {
	for _, files := range []int{50, 500} {
		b.Run(fmt.Sprintf("%dfiles", files), func(b *testing.B) {
			repo := benchRepo(b, files)
			ck := benchCheckpointer(b, repo)
			ctx := context.Background()
			snap := []byte(`[{"role":"user","content":"work"}]`)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := ck.Checkpoint(ctx, snap); err != nil {
					b.Fatalf("Checkpoint: %v", err)
				}
			}
		})
	}
}

// BenchmarkTurnCheckpoint_DirtyTree measures the case where the previous
// turn modified a file: the change is staged and captured by the stash.
func BenchmarkTurnCheckpoint_DirtyTree(b *testing.B) {
	for _, files := range []int{50, 500} {
		b.Run(fmt.Sprintf("%dfiles", files), func(b *testing.B) {
			repo := benchRepo(b, files)
			ck := benchCheckpointer(b, repo)
			ctx := context.Background()
			snap := []byte(`[{"role":"user","content":"work"}]`)
			target := filepath.Join(repo, "pkg", "file_0000.go")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := os.WriteFile(target, []byte(fmt.Sprintf("dirty %d", i)), 0o644); err != nil {
					b.Fatalf("write dirty file: %v", err)
				}
				if _, err := ck.Checkpoint(ctx, snap); err != nil {
					b.Fatalf("Checkpoint: %v", err)
				}
			}
		})
	}
}

// BenchmarkTurnCheckpointRestore measures the full rewind cycle on a
// dirty tree: checkpoint, simulate a turn's file churn, then restore.
// This is the cost paid when a failed turn is actually rewound.
func BenchmarkTurnCheckpointRestore(b *testing.B) {
	for _, files := range []int{50, 500} {
		b.Run(fmt.Sprintf("%dfiles", files), func(b *testing.B) {
			repo := benchRepo(b, files)
			ck := benchCheckpointer(b, repo)
			ctx := context.Background()
			snap := []byte(`[{"role":"user","content":"work"}]`)
			target := filepath.Join(repo, "pkg", "file_0001.go")
			junk := filepath.Join(repo, "pkg", "junk_new.txt")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				id, err := ck.Checkpoint(ctx, snap)
				if err != nil {
					b.Fatalf("Checkpoint: %v", err)
				}
				if err := os.WriteFile(target, []byte("churn"), 0o644); err != nil {
					b.Fatalf("churn write: %v", err)
				}
				if err := os.WriteFile(junk, []byte("junk"), 0o644); err != nil {
					b.Fatalf("junk write: %v", err)
				}
				if _, err := ck.Restore(ctx, id); err != nil {
					b.Fatalf("Restore: %v", err)
				}
			}
		})
	}
}
