package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/confinement"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
)

// TestConfinementScratchEnv_Redirects: the redirect points the housekeeping keys
// inside the worktree and creates the directories.
func TestConfinementScratchEnv_Redirects(t *testing.T) {
	root := t.TempDir()
	env, err := ConfinementScratchEnv(root)
	if err != nil {
		t.Fatalf("scratch env: %v", err)
	}
	want := map[string]string{
		"HOME":             filepath.Join(root, confinementScratchName, "home"),
		"TMPDIR":           filepath.Join(root, confinementScratchName, "tmp"),
		"XDG_CACHE_HOME":   filepath.Join(root, confinementScratchName, "cache"),
		"GOCACHE":          filepath.Join(root, confinementScratchName, "go-build"),
		"npm_config_cache": filepath.Join(root, confinementScratchName, "npm"),
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if w, ok := want[k]; ok {
			if v != w {
				t.Errorf("%s = %q, want %q", k, v, w)
			}
			fi, err := os.Stat(v)
			if err != nil || !fi.IsDir() {
				t.Errorf("%s dir %q not created: %v", k, v, err)
			} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
				t.Errorf("%s dir perm = %o, want 700 (HOME/TMPDIR may hold credentials)", k, fi.Mode().Perm())
			}
			delete(want, k)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing redirect keys: %v", want)
	}
}

// TestDefaultGoModCache pins the pre-redirect resolution: the HOME redirect
// would otherwise relocate Go's module cache into the scratch.
func TestDefaultGoModCache(t *testing.T) {
	t.Setenv("GOPATH", "")
	t.Setenv("HOME", "/real/home")
	if got, want := defaultGoModCache(), filepath.Join("/real/home", "go", "pkg", "mod"); got != want {
		t.Errorf("default = %q, want %q", got, want)
	}
	t.Setenv("GOPATH", "/go")
	if got, want := defaultGoModCache(), filepath.Join("/go", "pkg", "mod"); got != want {
		t.Errorf("with GOPATH = %q, want %q", got, want)
	}
}

// TestConfinementScratchEnv_PointsGitAtGlobalConfig: the redirect moves HOME, so
// git would look for the user's identity in an empty scratch and refuse to commit
// ("Author identity unknown"). The redirect points git at the REAL global config
// instead — no copy, so a global config carrying `http.<url>.extraheader` tokens
// never lands in the worktree, and `include` paths keep resolving against the
// file's real location.
func TestConfinementScratchEnv_PointsGitAtGlobalConfig(t *testing.T) {
	// No inherited pointer: the resolution under test is the HOME one.
	if prev, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		_ = os.Unsetenv("GIT_CONFIG_GLOBAL")
		t.Cleanup(func() { _ = os.Setenv("GIT_CONFIG_GLOBAL", prev) })
	}
	gitGlobal := func(t *testing.T, root string) (string, bool) {
		t.Helper()
		env, err := ConfinementScratchEnv(root)
		if err != nil {
			t.Fatalf("scratch env: %v", err)
		}
		for _, kv := range env {
			if k, v, _ := strings.Cut(kv, "="); k == "GIT_CONFIG_GLOBAL" {
				return v, true
			}
		}
		return "", false
	}

	t.Run("legacy .gitconfig wins when both exist", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		legacy := filepath.Join(home, ".gitconfig")
		if err := os.WriteFile(legacy, []byte("[user]\n\tname = Legacy\n"), 0o644); err != nil {
			t.Fatalf("seed .gitconfig: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(home, ".config", "git"), 0o755); err != nil {
			t.Fatalf("seed xdg dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(home, ".config", "git", "config"), []byte("[user]\n\tname = XDG\n"), 0o644); err != nil {
			t.Fatalf("seed xdg config: %v", err)
		}
		root := t.TempDir()
		got, ok := gitGlobal(t, root)
		if !ok {
			t.Fatal("GIT_CONFIG_GLOBAL must be set when a global config exists")
		}
		if got != legacy {
			t.Errorf("GIT_CONFIG_GLOBAL = %q, want the legacy config git applies last (%q)", got, legacy)
		}
		// Nothing is copied into the worktree: the config can carry credentials.
		if _, err := os.Stat(filepath.Join(root, confinementScratchName, "home", ".gitconfig")); !os.IsNotExist(err) {
			t.Errorf("the global config must NOT be copied into the worktree (stat err=%v)", err)
		}
	})

	t.Run("XDG_CONFIG_HOME is honored", func(t *testing.T) {
		home := t.TempDir()
		xdg := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", xdg)
		if err := os.MkdirAll(filepath.Join(xdg, "git"), 0o755); err != nil {
			t.Fatalf("seed xdg dir: %v", err)
		}
		want := filepath.Join(xdg, "git", "config")
		if err := os.WriteFile(want, []byte("[user]\n\tname = XDG\n"), 0o644); err != nil {
			t.Fatalf("seed xdg config: %v", err)
		}
		if got, ok := gitGlobal(t, t.TempDir()); !ok || got != want {
			t.Errorf("GIT_CONFIG_GLOBAL = %q (set=%v), want %q", got, ok, want)
		}
	})

	t.Run("no global config adds no pointer", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		if got, ok := gitGlobal(t, t.TempDir()); ok {
			t.Errorf("no source config should mean no pointer, got %q", got)
		}
	})
}

// TestConfinementScratchEnv_RefusesSymlinkedScratch: the scratch is created by
// the unconfined parent, and MkdirAll follows symlinks — so a `.yaah-scratch`
// planted as a symlink out of the worktree (a checked-in one, or one an earlier
// turn left) must be refused rather than followed, or the redirect would point
// the body outside the jail's writable root.
func TestConfinementScratchEnv_RefusesSymlinkedScratch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	outside := t.TempDir()
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, confinementScratchName)); err != nil {
		t.Fatalf("plant the symlink: %v", err)
	}

	if _, err := ConfinementScratchEnv(root); err == nil {
		t.Fatal("a symlinked scratch must be refused, not followed")
	}
	// Nothing may have been created through it.
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Errorf("the symlink target must be untouched: entries=%v err=%v", entries, err)
	}

	// A final-component symlink is refused too (not just a parent).
	root2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root2, confinementScratchName), 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root2, confinementScratchName, "home")); err != nil {
		t.Fatalf("plant the inner symlink: %v", err)
	}
	if _, err := ConfinementScratchEnv(root2); err == nil {
		t.Fatal("a symlinked scratch HOME must be refused, not followed")
	}
}

// TestScrubConfinementScratch_Removes: the scrub removes the whole scratch tree.
func TestScrubConfinementScratch_Removes(t *testing.T) {
	root := t.TempDir()
	if _, err := ConfinementScratchEnv(root); err != nil {
		t.Fatalf("scratch env: %v", err)
	}
	ScrubConfinementScratch(root)
	if _, err := os.Stat(filepath.Join(root, confinementScratchName)); !os.IsNotExist(err) {
		t.Fatalf("scrub should remove the scratch (stat err=%v)", err)
	}
}

// TestMergeEnv_OverridesWithoutDuplicates: overrides win by key and never
// duplicate it.
func TestMergeEnv_OverridesWithoutDuplicates(t *testing.T) {
	base := []string{"PATH=/bin", "HOME=/real/home", "LANG=C"}
	got := mergeEnv(base, []string{"HOME=/work/home", "TMPDIR=/work/tmp"})

	n := 0
	for _, kv := range got {
		if strings.HasPrefix(kv, "HOME=") {
			n++
			if kv != "HOME=/work/home" {
				t.Errorf("HOME = %q, want the override", kv)
			}
		}
	}
	if n != 1 {
		t.Errorf("HOME appears %d times, want 1 (duplicate keys resolve unpredictably)", n)
	}
	if !slices.Contains(got, "TMPDIR=/work/tmp") {
		t.Error("the new TMPDIR override is missing")
	}
	if !slices.Contains(got, "PATH=/bin") {
		t.Error("an unrelated base key must survive")
	}
}

// TestConfinedWorkspace_EnvRedirectLandsInWorktree: with the redirect, a shell
// writing $HOME and $TMPDIR lands inside the worktree; without it, the same
// write is denied (HOME is outside the jail). Linux only.
func TestConfinedWorkspace_EnvRedirectLandsInWorktree(t *testing.T) {
	backend := landlock.Detect()
	if backend == nil {
		t.Skip("no Landlock on this host")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell redirection")
	}
	root := t.TempDir()
	env, err := ConfinementScratchEnv(root)
	if err != nil {
		t.Fatalf("scratch env: %v", err)
	}
	ws := confinedTestWorkspace(root, backend, confinement.PlacementPaddock).WithEnv(env)

	res, err := ws.Exec(context.Background(), ExecRequest{
		Command: "sh",
		Args:    []string{"-c", `echo h > "$HOME/home.txt" && echo t > "$TMPDIR/tmp.txt" && echo DONE`},
	})
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, "DONE") {
		t.Fatalf("redirected $HOME/$TMPDIR writes should land: rc=%d out=%q err=%q runErr=%v",
			res.ExitCode, res.Stdout, res.Stderr, err)
	}
	if _, err := os.Stat(filepath.Join(root, confinementScratchName, "home", "home.txt")); err != nil {
		t.Errorf("$HOME write should land in the worktree scratch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, confinementScratchName, "tmp", "tmp.txt")); err != nil {
		t.Errorf("$TMPDIR write should land in the worktree scratch: %v", err)
	}

	// The redirect is load-bearing: without it, HOME is outside the jail.
	plain := confinedTestWorkspace(root, backend, confinement.PlacementPaddock)
	if res2, _ := plain.Exec(context.Background(), ExecRequest{
		Command: "sh",
		Args:    []string{"-c", `echo x > "$HOME/yaah-should-be-denied"`},
	}); res2.ExitCode == 0 {
		t.Error("without the redirect, a $HOME write outside the worktree must be denied")
	}
}
