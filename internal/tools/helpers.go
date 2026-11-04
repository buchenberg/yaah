package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Shared constants used across multiple tools.

const bashDefaultTimeout = 30 * time.Second

// expandHomeDir replaces a leading ~/ or ~ with the user's home directory.
func expandHomeDir(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		return home
	}
	return path
}

// resolvePathWithPV resolves a user-supplied path through the PathValidator
// when one is configured, falling back to expandHomeDir (legacy behaviour).
// File-accessing tools should call this during Execute instead of
// expandHomeDir directly.
func resolvePathWithPV(pv *PathValidator, input string) (string, error) {
	if pv != nil {
		return pv.ResolvePath(input)
	}
	if input == "" {
		return ".", nil
	}
	return expandHomeDir(input), nil
}

// rgAvailable checks if ripgrep is on PATH.
func rgAvailable() bool {
	_, err := exec.LookPath("rg")
	return err == nil
}

// commonIgnoreDirs lists directories that are nearly always safe to skip
// during filesystem walks (glob, grep, ls).
//
// Harness-owned state and retained dispatch worlds are included: they hold
// stale copies of the repository, so searching them returns duplicate hits
// with wrong line numbers. dist/build/bin/obj are deliberately NOT listed —
// they are valid Go package names and a project may keep source there.
var commonIgnoreDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".mypy_cache": true, ".pytest_cache": true,
	".tox": true, ".eggs": true,
	".next": true, ".nuxt": true, ".output": true,
	"vendor": true, ".idea": true, ".vscode": true,
	"target": true, ".dart_tool": true, ".turbo": true,

	// yaah state and dispatch worlds (retained worktrees, proposals, scratch).
	".yaah": true, ".kilo": true, ".scratch": true,
	// Other coding-tool state directories observed in the wild.
	".entire": true, ".serena": true, ".qwen": true, ".runtime": true,
	".sandbox": true, ".playwright-mcp": true, ".release": true,
	".codex": true, ".claude": true, ".ghost": true, ".hermes": true,
}

// isNestedWorktreeRoot reports whether path is the root of a git worktree
// nested inside the directory being walked. A linked worktree has a .git
// file (not a directory) pointing at the main repository's gitdir. Skipping
// it structurally covers dispatch worlds mounted under a name we do not
// know, while still letting an explicit search of the worktree root work.
func isNestedWorktreeRoot(ctx context.Context, ws Workspace, path string) bool {
	info, err := ws.Lstat(ctx, ws.Join(path, ".git"))
	return err == nil && !info.IsDir()
}

// nestedWorktreeFilter drops ripgrep results that live under a linked
// worktree nested below the search root. Ripgrep's glob language cannot
// express "this directory contains a .git file", so filtering the results
// keeps the rg path consistent with the native walker without a second tree
// scan. Results are checked lazily and per-directory answers are cached.
type nestedWorktreeFilter struct {
	ctx  context.Context
	ws   Workspace
	root string
	seen map[string]bool
}

func newNestedWorktreeFilter(ctx context.Context, ws Workspace, root string) *nestedWorktreeFilter {
	return &nestedWorktreeFilter{
		ctx:  ctx,
		ws:   ws,
		root: filepath.Clean(root),
		seen: make(map[string]bool),
	}
}

func (f *nestedWorktreeFilter) excluded(file string) bool {
	abs := file
	if !filepath.IsAbs(abs) {
		resolved, err := f.ws.ResolvePath(file)
		if err != nil {
			return false
		}
		abs = resolved
	}
	dir := filepath.Dir(filepath.Clean(abs))
	for {
		if sameFilePath(dir, f.root) {
			return false
		}
		if excluded, ok := f.seen[dir]; ok {
			if excluded {
				return true
			}
		} else {
			if !pathWithin(f.root, dir) {
				f.seen[dir] = false
				return false
			}
			if isNestedWorktreeRoot(f.ctx, f.ws, dir) {
				f.seen[dir] = true
				return true
			}
			f.seen[dir] = false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func sameFilePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// pathWithin reports whether child is inside root (or equal to it).
func pathWithin(root, child string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// binaryExtensions is a rough set of extensions that should be skipped
// during content searches.
var binaryExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
	".pdf": true, ".zip": true, ".gz": true, ".tar": true, ".bz2": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".class": true, ".pyc": true, ".pyo": true, ".o": true, ".obj": true,
	".db": true, ".sqlite": true, ".sqlite3": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true,
	".min.js": true, ".min.css": true, ".map": true,
}

// dangerousCommands is a coarse deny-list of obviously destructive shell
// patterns. It is NOT a security boundary: model-generated shell can trivially
// evade a substring deny-list (whitespace, shell variables, indirection,
// decoders). Real protection comes from the approval gate (config.Approval);
// this list only catches the most blatant mistakes.
var dangerousCommands = []string{
	"rm -rf /", "rm -rf ~", "rm -rf .",
	"shutdown", "reboot", "halt",
	"mkfs", "mkswap",
	"dd if=", ":(){ :|:& };:",
	"chmod 777 /", "chown -R",
	"remove-item -recurse -force c:\\",
	"format-volume", "stop-computer", "restart-computer",
	"clear-disk", "initialize-disk",
}

// isDangerous reports whether cmd matches a known destructive pattern. It is a
// best-effort guard only (see dangerousCommands).
func isDangerous(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, dangerous := range dangerousCommands {
		if strings.Contains(lower, dangerous) {
			return true
		}
	}
	return false
}
