package tools

import (
	"bytes"
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

// TestConfinementScratchEnv_CarriesGitIdentity: the redirect moves HOME, so git
// would look for the user's identity in an empty scratch and refuse to commit
// ("Author identity unknown"). The redirect carries the global config in at
// git's own precedence — both candidate files — and writes it 0o600, since a
// git config can name credential helpers and URL rewrites.
func TestConfinementScratchEnv_CarriesGitIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := []byte("[user]\n\tname = Legacy\n\temail = legacy@example.com\n")
	xdg := []byte("[user]\n\tname = XDG\n")
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), legacy, 0o644); err != nil {
		t.Fatalf("seed .gitconfig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config", "git"), 0o755); err != nil {
		t.Fatalf("seed config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "git", "config"), xdg, 0o644); err != nil {
		t.Fatalf("seed xdg config: %v", err)
	}

	root := t.TempDir()
	if _, err := ConfinementScratchEnv(root); err != nil {
		t.Fatalf("scratch env: %v", err)
	}

	scratchHome := filepath.Join(root, confinementScratchName, "home")
	for _, c := range []struct {
		rel  string
		want []byte
	}{
		{".gitconfig", legacy},
		{filepath.Join(".config", "git", "config"), xdg},
	} {
		dst := filepath.Join(scratchHome, c.rel)
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Errorf("%s not carried into the scratch: %v", c.rel, err)
			continue
		}
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s content = %q, want %q", c.rel, got, c.want)
		}
		if runtime.GOOS != "windows" {
			fi, statErr := os.Stat(dst)
			if statErr != nil {
				t.Errorf("%s: %v", c.rel, statErr)
			} else if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s perm = %o, want 600", c.rel, fi.Mode().Perm())
			}
		}
	}

	// An absent global config is the normal case, not an error: the scratch is
	// still built, just without one.
	empty := t.TempDir()
	t.Setenv("HOME", empty)
	root2 := t.TempDir()
	if _, err := ConfinementScratchEnv(root2); err != nil {
		t.Fatalf("scratch env with no global config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root2, confinementScratchName, "home", ".gitconfig")); !os.IsNotExist(err) {
		t.Errorf("no source config should mean no carried config (stat err=%v)", err)
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
