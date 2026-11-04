package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// preferNativeWalk reports whether a search must use the native walker even
// when ripgrep is available. Ripgrep's glob language cannot express "this
// directory contains a .git file", so a top-level linked worktree under an
// unknown name would otherwise be traversed. Checking the search root's
// immediate children is one ReadDir plus one Lstat per directory and keeps
// the common custom-worktree-root case correct on both code paths.
func preferNativeWalk(ctx context.Context, ws Workspace, root string) bool {
	entries, err := ws.ReadDir(ctx, root)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || commonIgnoreDirs[e.Name()] {
			continue
		}
		if isNestedWorktreeRoot(ctx, ws, ws.Join(root, e.Name())) {
			return true
		}
	}
	return false
}

// ripgrepIgnoreArgs returns negative --glob patterns for the known
// harness/tool directories. Ripgrep already skips hidden directories, but
// making the list explicit keeps the rg and native paths aligned for visible
// names such as node_modules, target, and vendor. A name that appears in the
// search root itself is omitted so an explicit search of that directory still
// works (the native walker never skips its root either).
func ripgrepIgnoreArgs(root string) []string {
	skip := make(map[string]bool)
	for _, part := range strings.Split(filepath.ToSlash(root), "/") {
		skip[part] = true
	}
	names := make([]string, 0, len(commonIgnoreDirs))
	for name := range commonIgnoreDirs {
		if skip[name] {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	args := make([]string, 0, len(names)*4)
	for _, name := range names {
		args = append(args,
			"--glob", "!"+name+"/**",
			"--glob", "!**/"+name+"/**")
	}
	return args
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
