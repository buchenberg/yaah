package tools

import (
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
		// 0o700: HOME and TMPDIR can hold credentials (npm tokens, cloud
		// configs) — nothing outside the sandbox should read them.
		if err := os.MkdirAll(dir, 0o700); err != nil {
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

	// Carry the git identity (see carryGitConfig): the redirect moves HOME, and
	// with it the global config git finds the user's name and email in — so
	// without this a confined `git commit` fails with "Author identity unknown".
	carryGitConfig(scratch)
	return env, nil
}

// gitConfigCarry is the global git config git would read from a normal HOME, at
// the relative paths it looks in: $XDG_CONFIG_HOME/git/config (worked out below
// against the pre-redirect HOME) then ~/.gitconfig. Both are carried, in order,
// so git's own precedence is preserved rather than flattened.
var gitConfigCarry = [][2]string{
	{filepath.Join(".config", "git", "config"), filepath.Join("home", ".config", "git", "config")},
	{".gitconfig", filepath.Join("home", ".gitconfig")},
}

// carryGitConfig copies the user's global git config into the scratch HOME so a
// confined git keeps the identity it has on the host. The redirect moves HOME
// for writes, but the global identity is a READ the body still needs — a commit
// without it fails outright ("Author identity unknown"), which is the one
// silent-but-total regression the redirect could cause.
//
// It copies rather than pointing git at the real file (GIT_CONFIG_GLOBAL) so
// git's own precedence — both candidate files — is preserved, and a
// global-config WRITE lands in the scratch (discarded with it) instead of being
// refused as an out-of-root write. It deliberately does NOT carry credential
// stores (~/.git-credentials, ~/.npmrc, ~/.netrc): copying secrets into a
// proposal-bearing worktree is a worse trade than requiring the user to
// configure the worktree, and authentication is an authority concern, not a
// redirect one.
//
// Best-effort: an absent source is the normal case and a copy failure degrades
// to "no global config", never a failed dispatch. The scratch is scrubbed
// before the workspace is sealed, so the copy never reaches a proposal's delta.
func carryGitConfig(scratch string) {
	home := os.Getenv("HOME")
	if home == "" {
		return
	}
	for _, c := range gitConfigCarry {
		data, err := os.ReadFile(filepath.Join(home, c[0]))
		if err != nil {
			continue
		}
		dst := filepath.Join(scratch, c[1])
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			continue
		}
		// 0o600: a git config can name credential helpers and URL rewrites.
		_ = os.WriteFile(dst, data, 0o600)
	}
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
