package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// confinement.go holds the retained lane's environment redirect. A confined
// workspace admits exactly one writable root (the worktree), so housekeeping
// writes — HOME, TMPDIR, build caches — are redirected INTO it rather than the
// jail being widened. This mirrors Python's provider env redirect
// (HOME/CLAUDE_CONFIG_DIR/TMPDIR -> <working_path>/.claude-scratch).
//
// The scratch is scrubbed before the workspace is sealed, so a proposal's delta
// is the sub-agent's intended writes only.

// confinementScratchName is the in-worktree directory the redirect points at.
const confinementScratchName = ".yaah-scratch"

// ConfinementScratchEnv creates the redirect scratch beneath workdir and returns
// the environment that points housekeeping writes into it, covering the common
// build tools: HOME (npm/cargo/etc. config), TMPDIR, XDG_CACHE_HOME (pip and
// many CLIs), GOCACHE (Go), npm_config_cache.
//
// GOMODCACHE is deliberately NOT redirected: Landlock governs writes only, so a
// shared module cache stays readable and the confined body needs no write to it —
// only a *download* of a not-yet-present module would. Populate that (and any
// other build input) via the worktree bootstrap, which runs unconfined before
// the body.
func ConfinementScratchEnv(workdir string) ([]string, error) {
	if workdir == "" {
		return nil, nil
	}
	scratch := filepath.Join(workdir, confinementScratchName)
	specs := []struct{ key, sub string }{
		{"HOME", "home"},
		{"TMPDIR", "tmp"},
		{"XDG_CACHE_HOME", "cache"},
		{"GOCACHE", "go-build"},
		{"npm_config_cache", "npm"},
	}
	env := make([]string, 0, len(specs)+1)
	for _, s := range specs {
		dir := filepath.Join(scratch, s.sub)
		if err := confinementScratchDir(workdir, dir); err != nil {
			return nil, err
		}
		env = append(env, s.key+"="+dir)
	}

	// Pin GOMODCACHE, which the HOME redirect would otherwise relocate: Go
	// defaults the module cache to $HOME/go/pkg/mod, so moving HOME silently
	// moves it into the scratch (a cold, re-downloading cache). Resolve it HERE,
	// against the pre-redirect environment, and pass it through unchanged — the
	// body then reads the shared cache. Landlock governs writes only, so the
	// read is unpoliced; a *download* of a missing module needs a write, which
	// the (unconfined) worktree bootstrap does.
	if _, set := os.LookupEnv("GOMODCACHE"); !set {
		env = append(env, "GOMODCACHE="+defaultGoModCache())
	}

	// Point git at the user's REAL global config (see realGitGlobalConfig): the
	// redirect moves HOME, and with it the global config git finds the user's
	// identity in — so without this a confined `git commit` fails with "Author
	// identity unknown".
	if _, set := os.LookupEnv("GIT_CONFIG_GLOBAL"); !set {
		if p := realGitGlobalConfig(); p != "" {
			env = append(env, "GIT_CONFIG_GLOBAL="+p)
		}
	}
	return env, nil
}

// realGitGlobalConfig resolves the file git would read as the user's global
// config, against the PRE-redirect environment: $GIT_CONFIG_GLOBAL if set, else
// ~/.gitconfig, else $XDG_CONFIG_HOME/git/config (or ~/.config/git/config).
// Empty when the user has none — the normal case.
//
// The legacy ~/.gitconfig is preferred when both exist: git reads the XDG file
// first and the legacy one after, so the legacy values are the ones that win —
// pointing at it preserves the effective identity. (The path is resolved here
// because both candidates are HOME-relative, and HOME is about to move.)
func realGitGlobalConfig() string {
	if p := os.Getenv("GIT_CONFIG_GLOBAL"); p != "" {
		return p
	}
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	for _, p := range []string{
		filepath.Join(home, ".gitconfig"),
		filepath.Join(dir, "git", "config"),
	} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// confinementScratchDir prepares one scratch directory beneath root, refusing any
// path that is, or resolves through, a symlink out of the worktree.
//
// The worktree is seeded from the repository (and reused across turns), so a
// checked-in `.yaah-scratch` symlink — or one an earlier turn planted — would
// otherwise redirect the redirect: MkdirAll follows symlinks, and the body's
// HOME/TMPDIR would then point outside the jail's writable root. These writes
// happen in the unconfined parent, so walk the components from the worktree down
// and refuse the first symlink BEFORE creating anything. 0o700 because HOME and
// TMPDIR can hold credentials (npm tokens, cloud configs).
func confinementScratchDir(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	cur := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			// Everything from here down is created fresh, so there is no symlink
			// to follow.
			break
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("confinement scratch %q is a symlink — refusing to redirect writes through it", cur)
		}
		if !fi.IsDir() {
			return fmt.Errorf("confinement scratch %q exists and is not a directory", cur)
		}
	}
	return os.MkdirAll(dir, 0o700)
}

// defaultGoModCache resolves Go's default module cache against the CURRENT
// (pre-redirect) environment: $GOMODCACHE if set, else $GOPATH/pkg/mod, else
// $HOME/go/pkg/mod.
func defaultGoModCache() string {
	if p := os.Getenv("GOPATH"); p != "" {
		return filepath.Join(p, "pkg", "mod")
	}
	return filepath.Join(os.Getenv("HOME"), "go", "pkg", "mod")
}

// ScrubConfinementScratch removes the redirect scratch so the sealed proposal
// excludes housekeeping writes. Best-effort: a leftover scratch shows up in the
// delta, which is cosmetic, not a hazard.
func ScrubConfinementScratch(workdir string) {
	if workdir == "" {
		return
	}
	_ = os.RemoveAll(filepath.Join(workdir, confinementScratchName))
}

// mergeEnv applies overrides to base by key (later wins), never duplicating a
// key: a child environment with duplicate keys resolves unpredictably.
func mergeEnv(base, overrides []string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, len(base))
	copy(out, base)
	idx := make(map[string]int, len(out))
	for i, kv := range out {
		if k, _, ok := strings.Cut(kv, "="); ok {
			idx[k] = i
		}
	}
	for _, kv := range overrides {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if i, exists := idx[k]; exists {
			out[i] = kv
			continue
		}
		idx[k] = len(out)
		out = append(out, kv)
	}
	return out
}
