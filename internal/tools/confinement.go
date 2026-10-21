package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	// POSIX variables first: every member module moves housekeeping writes
	// under these.
	specs := []struct{ key, sub string }{
		{"HOME", "home"},
		{"TMPDIR", "tmp"},
		{"XDG_CACHE_HOME", "cache"},
		{"GOCACHE", "go-build"},
		{"npm_config_cache", "npm"},
	}
	if runtime.GOOS == "windows" {
		// winacl's Launch passes the caller's environment through unchanged, so
		// the variables Windows tools write through (TEMP/TMP, and the profile
		// trees APPDATA/LOCALAPPDATA/USERPROFILE) would point outside the
		// writable root — the mirror of the POSIX denial loop this redirect
		// exists to prevent. Redirect them into the scratch; reads (not
		// governed by the mechanism) still reach the real trees exactly like a
		// redirected HOME does. GOMODCACHE is pinned once below for both
		// platforms; the git-config shim below covers the moved USERPROFILE
		// the way HOME's shim does on POSIX — realGitGlobalConfigs resolves
		// the pre-redirect home through os.UserHomeDir (%USERPROFILE% on
		// Windows) when HOME is unset, which native Windows typically is.
		specs = append(specs,
			struct{ key, sub string }{"TEMP", "tmp"},
			struct{ key, sub string }{"TMP", "tmp"},
			struct{ key, sub string }{"APPDATA", "appdata"},
			struct{ key, sub string }{"LOCALAPPDATA", "localappdata"},
			struct{ key, sub string }{"USERPROFILE", "home"},
		)
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
	//
	// When no home resolves at all (HOME unset, UserHomeDir failed — the
	// degenerate Unix case), the pin is left OFF rather than exporting a
	// relative "go/pkg/mod", which Go would resolve against the body's cwd and
	// download the cache into the worktree, defeating the pin AND polluting the
	// sealed delta. In that case the body's Go resolves its default from the
	// redirected HOME — a cold cache under the scratch, which re-downloads on
	// cold worktrees but is scrubbed before sealing. The pin is simply
	// unavailable in this case; no relative path is exported here.
	if _, set := os.LookupEnv("GOMODCACHE"); !set {
		if mc := defaultGoModCache(); mc != "" {
			env = append(env, "GOMODCACHE="+mc)
		}
	}

	// Point git at the user's REAL global config (see scratchGitGlobalConfig): the
	// redirect moves HOME, and with it the global config git finds the user's
	// identity in — so without this a confined `git commit` fails with "Author
	// identity unknown".
	if p := scratchGitGlobalConfig(scratch); p != "" {
		env = append(env, "GIT_CONFIG_GLOBAL="+p)
	}
	return env, nil
}

// scratchGitGlobalConfig writes a shim into the scratch HOME that INCLUDES the
// user's real global config files, and returns its path (or "" when there is
// nothing to include, or the user already points git somewhere else and the
// inherited value should be left alone).
//
// A shim, rather than pointing at a real file, because GIT_CONFIG_GLOBAL
// *replaces* BOTH default files and takes exactly one path — a colon-separated
// list is not honored (git 2.55 reads the whole string as one filename and
// silently finds nothing), so naming a single file would drop the other, and a
// user whose name lives in ~/.gitconfig and email in $XDG_CONFIG_HOME/git/config
// would still get "Author identity unknown". The shim includes them in git's own
// order (XDG first, legacy second, so legacy wins per key) and carries paths
// rather than values, so no config content — `http.<url>.extraheader` tokens
// included — is copied into the worktree. Each included file's own `include`
// paths still resolve against its real location, and a file that disappears
// mid-run is skipped rather than fatal.
func scratchGitGlobalConfig(scratch string) string {
	if _, set := os.LookupEnv("GIT_CONFIG_GLOBAL"); set {
		// Already pointed somewhere (possibly at an empty value); leave it.
		return ""
	}
	paths := realGitGlobalConfigs()
	if len(paths) == 0 {
		return ""
	}

	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "[include]\n\tpath = %s\n", gitConfigValue(p))
	}

	// The scratch HOME was just created and symlink-checked, but a repository
	// can carry a `.yaah-scratch/home/.gitconfig` — including a symlink — and
	// this write happens in the unconfined parent. Remove first, then create
	// exclusively, so we never write through a planted link.
	shim := filepath.Join(scratch, "home", ".gitconfig")
	_ = os.Remove(shim)
	f, err := os.OpenFile(shim, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// Best-effort: git then sees an empty HOME, so a commit needs an explicit
		// identity. A degraded run, not a failed dispatch.
		return ""
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return ""
	}
	return shim
}

// gitConfigValue quotes a path for a git config value. Unquoted interpolations
// are not safe: git starts a comment at `#`/`;` preceded by whitespace, and
// processes backslash escapes — so a HOME containing either would silently
// truncate or corrupt the shim. Quoting with `\`/`"`/newline/tab escaped makes
// the value exact.
//
// Bytes, not runes: a path is a byte string, and on Linux it may not be valid
// UTF-8. Iterating runes would replace every undecodable byte with U+FFFD and
// point git at a different file.
func gitConfigValue(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// realGitGlobalConfigs resolves the global config files git would read from a
// normal HOME, against the PRE-redirect environment, in git's precedence order:
// $XDG_CONFIG_HOME/git/config (or ~/.config/git/config) first, ~/.gitconfig
// second. Empty when the user has none — the normal case.
//
// The home is resolved the way git itself does on every platform: $HOME when
// set, else the platform home directory (git-for-Windows derives its HOME from
// %USERPROFILE%, where native Windows may leave HOME unset entirely). On Unix
// with no HOME this still degrades to nil exactly as before.
func realGitGlobalConfigs() []string {
	home := os.Getenv("HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	if home == "" {
		return nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	var paths []string
	for _, p := range []string{
		filepath.Join(dir, "git", "config"),
		filepath.Join(home, ".gitconfig"),
	} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			paths = append(paths, p)
		}
	}
	return paths
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
// (pre-redirect) environment: $GOPATH/pkg/mod if GOPATH is set, else
// $HOME/go/pkg/mod — resolving the home the way Go does on every platform
// ($HOME on Unix; %USERPROFILE% behind os.UserHomeDir on Windows, where HOME
// is typically unset). Empty when nothing resolves, so the caller leaves
// GOMODCACHE unset instead of exporting a relative path.
func defaultGoModCache() string {
	if p := os.Getenv("GOPATH"); p != "" {
		return filepath.Join(p, "pkg", "mod")
	}
	home := os.Getenv("HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "go", "pkg", "mod")
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
