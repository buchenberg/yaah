package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dispatchSearchFixture builds a tree with one real hit and several
// dispatch-world copies of the same hit. Linked worktrees get a .git file
// (not a directory), which is how git marks them.
func dispatchSearchFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("real/real.go", "package real\n\n// needle\n")
	write(".yaah/shepherd-worktrees/proposal-1/hit.go", "package stale\n\n// needle\n")
	write(".kilo/worktrees/copy/hit.go", "package stale\n\n// needle\n")
	write(".scratch/hit.go", "package stale\n\n// needle\n")
	write("custom-world/hit.go", "package stale\n\n// needle\n")
	write("custom-world/.git", "gitdir: /elsewhere/.git/worktrees/custom-world\n")
	// build/ is a legitimate package name and must remain searchable.
	write("build/foo.go", "package build\n")
	return tmp
}

func TestGlobNative_SkipsDispatchWorlds(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	out, err := (&GlobTool{}).globNative(context.Background(), "**/*.go", tmp)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if !strings.Contains(out, filepath.Join("real", "real.go")) {
		t.Errorf("real file missing from glob results:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join("build", "foo.go")) {
		t.Errorf("build/foo.go must stay searchable:\n%s", out)
	}
	for _, stale := range []string{".yaah", ".kilo", ".scratch", "custom-world"} {
		if strings.Contains(out, stale) {
			t.Errorf("glob descended into %s:\n%s", stale, out)
		}
	}
}

func TestGlobNative_WorktreeRootStillSearchable(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	root := filepath.Join(tmp, "custom-world")
	out, err := (&GlobTool{}).globNative(context.Background(), "*.go", root)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if !strings.Contains(out, "hit.go") {
		t.Errorf("an explicit worktree-root search must not be skipped: %q", out)
	}
}

// TestPreferNativeWalk_DetectsLinkedWorktree keeps the ripgrep dispatch
// honest: a top-level linked worktree under an unknown name forces the
// native walker, which can skip it structurally.
func TestPreferNativeWalk_DetectsLinkedWorktree(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	if !preferNativeWalk(context.Background(), workspaceOf(nil, nil), tmp) {
		t.Fatal("top-level linked worktree should force the native walker")
	}
}

func TestRipgrepIgnoreArgs_ExplicitRootWins(t *testing.T) {
	inside := strings.Join(ripgrepIgnoreArgs(filepath.Join("repo", ".yaah")), " ")
	if strings.Contains(inside, "!.yaah/**") || strings.Contains(inside, "!**/.yaah/**") {
		t.Errorf("explicit root under .yaah must not be excluded:\n%s", inside)
	}

	normal := strings.Join(ripgrepIgnoreArgs("repo"), " ")
	if !strings.Contains(normal, "!**/.yaah/**") {
		t.Errorf("normal search should exclude .yaah:\n%s", normal)
	}
}

func TestGrepNative_SkipsDispatchWorlds(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	out, err := (&GrepTool{}).grepNative(context.Background(), "needle", tmp, "")
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if got := strings.Count(out, "needle"); got != 1 {
		t.Errorf("expected exactly the real hit, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, filepath.Join("real", "real.go")) {
		t.Errorf("real hit missing:\n%s", out)
	}
	for _, stale := range []string{".yaah", ".kilo", ".scratch", "custom-world"} {
		if strings.Contains(out, stale) {
			t.Errorf("grep descended into %s:\n%s", stale, out)
		}
	}
}

func TestLs_SkipsDispatchWorlds(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	args, _ := json.Marshal(map[string]any{"path": tmp})
	out, err := (&LsTool{}).Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	for _, want := range []string{"real/", "build/"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %s in ls output:\n%s", want, out)
		}
	}
	for _, stale := range []string{".yaah", ".kilo", ".scratch", "custom-world"} {
		if strings.Contains(out, stale) {
			t.Errorf("ls listed %s:\n%s", stale, out)
		}
	}
}

// TestReplace_SkipsNestedWorktree pins the write-side counterpart of
// yaah-8kub: replace must not mutate a stale linked worktree under a name
// that is not in commonIgnoreDirs.
func TestReplace_SkipsNestedWorktree(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	args, _ := json.Marshal(map[string]any{
		"pattern":     "package stale",
		"replacement": "package changed",
		"path":        tmp,
		"include":     "*.go",
	})
	if _, err := (&ReplaceTool{}).Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("replace: %v", err)
	}

	copies := [][]string{
		{".yaah", "shepherd-worktrees", "proposal-1", "hit.go"},
		{".kilo", "worktrees", "copy", "hit.go"},
		{".scratch", "hit.go"},
		{"custom-world", "hit.go"},
	}
	for _, rel := range copies {
		data, err := os.ReadFile(filepath.Join(append([]string{tmp}, rel...)...))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "package changed") {
			t.Errorf("replace mutated dispatch-world copy %s", filepath.Join(rel...))
		}
	}
}
