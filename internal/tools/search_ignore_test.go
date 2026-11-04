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
	args, _ := json.Marshal(map[string]any{"pattern": "**/*.go", "path": tmp})
	out, err := (&GlobTool{}).Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if !strings.Contains(out, "real/real.go") {
		t.Errorf("real file missing from glob results:\n%s", out)
	}
	if !strings.Contains(out, "build/foo.go") {
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
	args, _ := json.Marshal(map[string]any{"pattern": "*.go", "path": root})
	out, err := (&GlobTool{}).Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if !strings.Contains(out, "hit.go") {
		t.Errorf("an explicit worktree-root search must not be skipped: %q", out)
	}
}

func TestGrepNative_SkipsDispatchWorlds(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	args, _ := json.Marshal(map[string]any{"pattern": "needle", "path": tmp})
	out, err := (&GrepTool{}).Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if got := strings.Count(out, "needle"); got != 1 {
		t.Errorf("expected exactly the real hit, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "real/real.go") {
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
