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
	write("sub/custom-world/hit.go", "package stale\n\n// needle\n")
	write("sub/custom-world/.git", "gitdir: /elsewhere/.git/worktrees/sub-custom-world\n")
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

// TestNestedWorktreeFilter_DropsNestedResults exercises the in-memory
// ancestor walk directly, including a deeper (two-level) nested worktree.
func TestNestedWorktreeFilter_DropsNestedResults(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	ws := workspaceOf(nil, nil)
	f := &nestedWorktreeFilter{
		ws:   ws,
		root: filepath.Clean(tmp),
		roots: map[string]bool{
			filepath.Clean(filepath.Join(tmp, "custom-world")):        true,
			filepath.Clean(filepath.Join(tmp, "sub", "custom-world")): true,
		},
	}
	if !f.excluded(filepath.Join(tmp, "custom-world", "hit.go")) {
		t.Error("top-level linked-worktree result should be excluded")
	}
	if !f.excluded(filepath.Join(tmp, "sub", "custom-world", "hit.go")) {
		t.Error("deeply nested linked-worktree result should be excluded")
	}
	if f.excluded(filepath.Join(tmp, "real", "real.go")) {
		t.Error("real result must be kept")
	}
}

func TestNestedWorktreeFilter_ExplicitRootSearchAllowed(t *testing.T) {
	tmp := dispatchSearchFixture(t)
	root := filepath.Join(tmp, "custom-world")
	f := &nestedWorktreeFilter{ws: workspaceOf(nil, nil), root: filepath.Clean(root), roots: map[string]bool{}}
	if f.excluded(filepath.Join(root, "hit.go")) {
		t.Error("an explicit search rooted at a worktree must keep its results")
	}
}

// TestIsNestedWorktreeRoot_DistinguishesSubmodule pins the .git file
// distinction: a linked worktree's gitdir points under .git/worktrees, a
// submodule's under .git/modules. Only the former is a stale dispatch copy.
func TestIsNestedWorktreeRoot_DistinguishesSubmodule(t *testing.T) {
	tmp := t.TempDir()
	wt := filepath.Join(tmp, "wt")
	sub := filepath.Join(tmp, "sub")
	for _, d := range []string{wt, sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /x/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := workspaceOf(nil, nil)
	if !isNestedWorktreeRoot(context.Background(), ws, wt) {
		t.Error("linked worktree should be detected")
	}
	if isNestedWorktreeRoot(context.Background(), ws, sub) {
		t.Error("submodule must not be treated as a nested worktree")
	}
}

// TestPosixRel pins the POSIX relative-path helper, including the root "/"
// case where filepath.Rel-style splitting would otherwise emit a stray "..".
func TestPosixRel(t *testing.T) {
	cases := []struct{ base, target, want string }{
		{"/", "/a", "a"},
		{"/", "/a/b", "a/b"},
		{"/a", "/a/b", "b"},
		{"/a", "/b", "../b"},
		{"/a", "/a", "."},
		{"/a/b", "/a/b/c", "c"},
	}
	for _, tc := range cases {
		got, err := posixRel(tc.base, tc.target)
		if err != nil {
			t.Fatalf("posixRel(%q, %q): %v", tc.base, tc.target, err)
		}
		if got != tc.want {
			t.Errorf("posixRel(%q, %q) = %q, want %q", tc.base, tc.target, got, tc.want)
		}
	}
}

// TestIsWorktreeGitdir pins the gitdir parse: only a gitdir whose
// second-to-last component is "worktrees" is a linked worktree, regardless of
// whether the repository path elsewhere contains the word "worktrees".
func TestIsWorktreeGitdir(t *testing.T) {
	cases := []struct {
		content string
		want    bool
	}{
		{"gitdir: /x/.git/worktrees/wt\n", true},
		{"gitdir: ../.git/modules/sub\n", false},
		{"gitdir: /home/worktrees/proj/.git/modules/sub\n", false},
		{"gitdir: /home/worktrees/proj/.git/worktrees/wt\n", true},
		{"gitdir: /x/.git/modules/worktrees/foo\n", false},
		{"gitdir: C:/repo/.git/worktrees/wt\n", true},
		{"gitdir: C:\\repo\\.git\\worktrees\\wt\n", true},
		{"not a gitdir line\n", false},
	}
	for _, tc := range cases {
		if got := isWorktreeGitdir(tc.content); got != tc.want {
			t.Errorf("isWorktreeGitdir(%q) = %v, want %v", tc.content, got, tc.want)
		}
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

// installFakeRipgrep puts a script named rg first on PATH. The script
// mimics ripgrep's discovery pass (listing .git files) and its --files and
// --null match output, so the ripgrep path is exercised deterministically on
// hosts without rg installed.
func installFakeRipgrep(t *testing.T, root string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake rg script uses a POSIX shell")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
case " $* " in
  *" **/.git "*)
    printf '%s\n' "$FAKE_RG_ROOT/custom-world/.git" "$FAKE_RG_ROOT/sub/custom-world/.git"
    ;;
  *" --files "*)
    printf '%s\n' "$FAKE_RG_ROOT/real/real.go" "$FAKE_RG_ROOT/custom-world/hit.go" "$FAKE_RG_ROOT/sub/custom-world/hit.go"
    ;;
  *" --no-heading "*)
    printf '%s\0%d:%s\n' "$FAKE_RG_ROOT/real/real.go" 3 "// needle"
    printf '%s\0%d:%s\n' "$FAKE_RG_ROOT/custom-world/hit.go" 3 "// needle"
    printf '%s\0%d:%s\n' "$FAKE_RG_ROOT/sub/custom-world/hit.go" 3 "// needle"
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
		{"sub", "custom-world", "hit.go"},
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
