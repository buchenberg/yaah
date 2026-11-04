package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

// TestNestedWorktreeFilter_DropsNestedResults keeps the ripgrep path's
// post-filter aligned with the native walker: a linked worktree under an
// unknown name is excluded, while the real tree is kept.
func TestNestedWorktreeFilter_DropsNestedResults(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	filter := newNestedWorktreeFilter(context.Background(), workspaceOf(nil, nil), tmp)
	if !filter.excluded(filepath.Join(tmp, "custom-world", "hit.go")) {
		t.Error("nested linked-worktree result should be excluded")
	}
	if filter.excluded(filepath.Join(tmp, "real", "real.go")) {
		t.Error("real result must be kept")
	}
}

func TestNestedWorktreeFilter_ExplicitRootSearchAllowed(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	root := filepath.Join(tmp, "custom-world")
	filter := newNestedWorktreeFilter(context.Background(), workspaceOf(nil, nil), root)
	if filter.excluded(filepath.Join(root, "hit.go")) {
		t.Error("an explicit search rooted at a worktree must keep its results")
	}
}

func TestGlobNative_ExplicitIgnoredRootStillSearchable(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	root := filepath.Join(tmp, ".yaah", "shepherd-worktrees", "proposal-1")
	out, err := (&GlobTool{}).globNative(context.Background(), "*.go", root)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if !strings.Contains(out, "hit.go") {
		t.Errorf("an explicit search of an ignored-looking root must work: %q", out)
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

func TestGrepResultLineRe_ParsesWindowsPaths(t *testing.T) {
	cases := []struct {
		line string
		file string
	}{
		{"real/real.go:3:// needle", "real/real.go"},
		{`C:\repo\real.go:12:needle`, `C:\repo\real.go`},
	}
	for _, tc := range cases {
		m := grepResultLineRe.FindStringSubmatch(tc.line)
		if m == nil || m[1] != tc.file {
			t.Errorf("grepResultLineRe(%q) = %v, want file %q", tc.line, m, tc.file)
		}
	}
}

// installFakeRipgrep puts a script named rg first on PATH. The script emits
// one real result and one linked-worktree result, which lets the ripgrep path
// be exercised deterministically on hosts without rg installed.
func installFakeRipgrep(t *testing.T, root string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake rg script uses a POSIX shell")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  --files)
    printf '%s\n' "$FAKE_RG_ROOT/real/real.go" "$FAKE_RG_ROOT/custom-world/hit.go"
    ;;
  --no-heading)
    printf '%s\n' "$FAKE_RG_ROOT/real/real.go:3:// needle" "$FAKE_RG_ROOT/custom-world/hit.go:3:// needle"
    ;;
  *)
    exit 2
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "rg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_RG_ROOT", root)
}

func TestGlobRipgrep_FiltersNestedWorktree(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	installFakeRipgrep(t, tmp)
	args, _ := json.Marshal(map[string]any{"pattern": "**/*.go", "path": tmp})
	out, err := (&GlobTool{}).Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if strings.Contains(out, "custom-world") {
		t.Errorf("ripgrep path returned a nested-worktree result:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join("real", "real.go")) {
		t.Errorf("ripgrep path dropped the real result:\n%s", out)
	}
}

func TestGrepRipgrep_FiltersNestedWorktree(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	installFakeRipgrep(t, tmp)
	args, _ := json.Marshal(map[string]any{"pattern": "needle", "path": tmp})
	out, err := (&GrepTool{}).Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if strings.Contains(out, "custom-world") {
		t.Errorf("ripgrep path returned a nested-worktree result:\n%s", out)
	}
	if got := strings.Count(out, "needle"); got != 1 {
		t.Errorf("ripgrep path should keep exactly the real hit, got %d:\n%s", got, out)
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
