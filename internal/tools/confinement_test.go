package tools

import (
	"context"
	"fmt"
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
// ("Author identity unknown"). The redirect points git at a SHIM in the scratch
// that includes the real global config files — not a copy, so a config carrying
// `http.<url>.extraheader` tokens never lands in the worktree, and `include`
// paths keep resolving against the files' real locations. A shim rather than a
// direct pointer, because GIT_CONFIG_GLOBAL replaces BOTH default files and
// takes one path: naming a single file would drop the other.
func TestConfinementScratchEnv_PointsGitAtGlobalConfig(t *testing.T) {
	// No inherited pointer: the resolution under test is the HOME one.
	if prev, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		_ = os.Unsetenv("GIT_CONFIG_GLOBAL")
		t.Cleanup(func() { _ = os.Setenv("GIT_CONFIG_GLOBAL", prev) })
	}
	// gitGlobal runs the redirect and returns the pointer plus the shim's body.
	gitGlobal := func(t *testing.T, root string) (string, string, bool) {
		t.Helper()
		env, err := ConfinementScratchEnv(root)
		if err != nil {
			t.Fatalf("scratch env: %v", err)
		}
		for _, kv := range env {
			if k, v, _ := strings.Cut(kv, "="); k == "GIT_CONFIG_GLOBAL" {
				body, rerr := os.ReadFile(v)
				if rerr != nil {
					t.Fatalf("the pointer must name a readable shim: %v", rerr)
				}
				return v, string(body), true
			}
		}
		return "", "", false
	}

	t.Run("both files are included, in git's precedence order", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		xdg := filepath.Join(home, ".config", "git", "config")
		legacy := filepath.Join(home, ".gitconfig")
		if err := os.MkdirAll(filepath.Dir(xdg), 0o755); err != nil {
			t.Fatalf("seed xdg dir: %v", err)
		}
		if err := os.WriteFile(xdg, []byte("[user]\n\tname = XDG\n"), 0o644); err != nil {
			t.Fatalf("seed xdg config: %v", err)
		}
		if err := os.WriteFile(legacy, []byte("[user]\n\temail = legacy@example.com\n"), 0o644); err != nil {
			t.Fatalf("seed .gitconfig: %v", err)
		}
		root := t.TempDir()
		pointer, body, ok := gitGlobal(t, root)
		if !ok {
			t.Fatal("GIT_CONFIG_GLOBAL must be set when a global config exists")
		}
		if want := filepath.Join(root, confinementScratchName, "home", ".gitconfig"); pointer != want {
			t.Errorf("GIT_CONFIG_GLOBAL = %q, want the shim %q", pointer, want)
		}
		// Both, in order. Including only one would DROP the other: the pointer
		// replaces both defaults rather than adding to them, and a name in one
		// file plus an email in the other is a real configuration.
		if !strings.Contains(body, "path = "+gitConfigValue(xdg)) || !strings.Contains(body, "path = "+gitConfigValue(legacy)) {
			t.Errorf("the shim must include both files:\n%s", body)
		}
		if strings.Index(body, xdg) > strings.Index(body, legacy) {
			t.Errorf("the shim must include the XDG file first (git's order):\n%s", body)
		}
		// Paths, not values: nothing from either config is copied.
		if strings.Contains(body, "XDG") || strings.Contains(body, "legacy@example.com") {
			t.Errorf("the shim must carry paths, not config content:\n%s", body)
		}
	})

	t.Run("a pre-set pointer is left alone", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = Legacy\n"), 0o644); err != nil {
			t.Fatalf("seed .gitconfig: %v", err)
		}
		t.Setenv("GIT_CONFIG_GLOBAL", "/somewhere/else")
		if pointer, _, ok := gitGlobal(t, t.TempDir()); ok {
			t.Errorf("an inherited pointer must be left alone, got %q", pointer)
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
		_, body, ok := gitGlobal(t, t.TempDir())
		if !ok {
			t.Fatal("GIT_CONFIG_GLOBAL must be set when a global config exists")
		}
		if !strings.Contains(body, "path = "+gitConfigValue(want)) {
			t.Errorf("the shim must include the XDG config %q:\n%s", want, body)
		}
	})

	t.Run("no global config adds no pointer", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		if pointer, _, ok := gitGlobal(t, t.TempDir()); ok {
			t.Errorf("no source config should mean no pointer, got %q", pointer)
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

// TestGitConfigValue_QuotesAndEscapes: an unquoted interpolation would be
// truncated at a comment character and reinterpreted at a backslash, so a HOME
// containing either would silently corrupt the shim.
func TestGitConfigValue_QuotesAndEscapes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/plain/.gitconfig", `"/home/plain/.gitconfig"`},
		{"/home/has space/.gitconfig", `"/home/has space/.gitconfig"`},
		{"/home/hash # not a comment/.gitconfig", `"/home/hash # not a comment/.gitconfig"`},
		{"/home/semi ; not a comment/.gitconfig", `"/home/semi ; not a comment/.gitconfig"`},
		{`/home/back\slash/.gitconfig`, `"/home/back\\slash/.gitconfig"`},
		{"/home/quote\"/.gitconfig", `"/home/quote\"/.gitconfig"`},
		{"/home/new\nline", `"/home/new\nline"`},
		// A path is a byte string and may not be valid UTF-8; every byte must survive.
		{"/home/\xff\xfe/.gitconfig", "\"/home/\xff\xfe/.gitconfig\""},
	}
	for _, c := range cases {
		if got := gitConfigValue(c.in); got != c.want {
			t.Errorf("gitConfigValue(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestConfinementHint_ExplainsRefusedWrites: a kernel refusal reaches the model as
// a bare "Permission denied" unless it is explained, and an unexplained refusal
// is what makes a jail feel broken. The hint names the writable root, and names
// the git rule when the refusal is git's own metadata.
func TestConfinementHint_ExplainsRefusedWrites(t *testing.T) {
	// Host-native absolute paths: the hint is only reachable where a kernel jail
	// runs (Linux today), but the function is cross-platform code and must not be
	// tested with one platform's path shapes.
	root := t.TempDir()
	outside := t.TempDir()

	gitCase := fmt.Sprintf("fatal: Unable to create '%s': Permission denied",
		filepath.Join(outside, ".git", "worktrees", "wt", "index.lock"))
	got := confinementHint(gitCase, root)
	if !strings.Contains(got, root) || !strings.Contains(got, "orchestrator commits") {
		t.Errorf("a git-metadata refusal must name the root and the commit rule, got:\n%s", got)
	}

	plain := fmt.Sprintf("sh: 1: cannot create %s: Permission denied", filepath.Join(outside, "motd"))
	if got := confinementHint(plain, root); !strings.Contains(got, root) || strings.Contains(got, "orchestrator commits") {
		t.Errorf("a non-git refusal outside the root must name the root without the git rule, got:\n%s", got)
	}

	// An apostrophe before the quoted path must not swallow the path.
	apos := fmt.Sprintf("error: can't open '%s': Permission denied", filepath.Join(outside, "x"))
	if got := confinementHint(apos, root); got == "" {
		t.Error("an unmatched apostrophe must not drop the refusal")
	}

	// A sibling is not "inside": <root>-other must not pass as the root.
	sibling := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-other", "x")
	if got := confinementHint("sh: 1: cannot create "+sibling+": Permission denied", root); got == "" {
		t.Error("a sibling of the root is outside it and must be explained")
	}

	// The false positives the reviewer named: a permission failure INSIDE the
	// worktree is not a boundary refusal, and captioning it as one misdirects.
	for _, s := range []string{
		"", // no failure at all
		"compile error: undefined: foo",
		"ls: cannot access '/nope': No such file or directory",
		// chmod 000 file, in-worktree
		"cat: " + filepath.Join(root, "secret") + ": Permission denied",
		// non-executable script, in-worktree
		"sh: 1: " + filepath.Join(root, "script.sh") + ": Permission denied",
		// read-only file, in-worktree
		"tee: " + filepath.Join(root, "ro.txt") + ": Read-only file system",
		// a refusal the message does not attribute to a path
		"git: Permission denied",
	} {
		if got := confinementHint(s, root); got != "" {
			t.Errorf("confinementHint(%q) = %q, want no hint (inside the root, or unattributable)", s, got)
		}
	}

	// No root to name (an advisory workspace runs no jail) — no hint.
	if got := confinementHint(gitCase, ""); got != "" {
		t.Errorf("a workspace with no root must produce no hint, got %q", got)
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
