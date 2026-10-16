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

	// Pinning GOMODCACHE, which the HOME redirect would otherwise relocate: Go
	// defaults the module cache to $HOME/go/pkg/mod, so moving HOME silently
	// moves it into the scratch (a cold, re-downloading cache). Resolve it HERE,
	// against the pre-redirect environment, and pass it through unchanged — the
	// body then reads the shared cache. Landlock governs writes only, so the
	// read is unpoliced; a *download* of a missing module needs a write, which
	// the (unconfined) worktree bootstrap does.
	if _, set := os.LookupEnv("GOMODCACHE"); !set {
		env = append(env, "GOMODCACHE="+defaultGoModCache())
	}
	return env, nil
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
