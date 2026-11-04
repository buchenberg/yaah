package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
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

// isNestedWorktreeRoot reports whether path is the root of a linked git
// worktree nested inside the directory being walked. A linked worktree has a
// .git file (not a directory) whose gitdir points under <main>/.git/worktrees.
// Skipping it structurally covers dispatch worlds mounted under a name we do
// not know, while still letting an explicit search of the worktree root work.
// A submodule also carries a .git file (pointing under .git/modules), but its
// sources are a legitimate part of the tree and must not be dropped.
//
// The probe is one Lstat per directory (plus a ReadFile only for directories
// that actually have a .git file). commonIgnoreDirs is checked before this, so
// the heavy dependency trees are skipped without the probe.
func isNestedWorktreeRoot(ctx context.Context, ws Workspace, path string) bool {
	gitPath := ws.Join(path, ".git")
	info, err := ws.Lstat(ctx, gitPath)
	if err != nil || info.IsDir() {
		return false
	}
	data, err := ws.ReadFile(ctx, gitPath)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "worktrees")
}

// wsPath* helpers route path manipulation through the workspace's own path
// semantics. A sandbox is POSIX regardless of the host OS (workspace.go:71),
// so applying filepath.Clean/Dir/IsAbs/Rel to sandbox paths would rewrite
// "/root/a" as "\root\a" on a Windows host and break the .git probe.
func wsPathClean(ws Workspace, p string) string {
	if ws.Local() {
		return filepath.Clean(p)
	}
	return path.Clean(p)
}

func wsPathDir(ws Workspace, p string) string {
	if ws.Local() {
		return filepath.Dir(p)
	}
	return path.Dir(p)
}

func wsPathIsAbs(ws Workspace, p string) bool {
	if ws.Local() {
		return filepath.IsAbs(p)
	}
	return path.IsAbs(p)
}

func wsPathRel(ws Workspace, base, target string) (string, error) {
	if ws.Local() {
		return filepath.Rel(base, target)
	}
	return posixRel(base, target)
}

// posixRel is the POSIX-path analogue of filepath.Rel, for sandbox workspaces
// (the path package has no Rel).
func posixRel(base, target string) (string, error) {
	base = path.Clean(base)
	target = path.Clean(target)
	if base == target {
		return ".", nil
	}
	b := splitPOSIXPath(base)
	t := splitPOSIXPath(target)
	i := 0
	for i < len(b) && i < len(t) && b[i] == t[i] {
		i++
	}
	var parts []string
	for j := i; j < len(b); j++ {
		parts = append(parts, "..")
	}
	parts = append(parts, t[i:]...)
	rel := strings.Join(parts, "/")
	if rel == "" {
		return ".", nil
	}
	return rel, nil
}

// splitPOSIXPath splits a clean POSIX path into non-empty components. The root
// "/" yields nil, so posixRel("/", child) produces the child without a leading
// "..".
func splitPOSIXPath(p string) []string {
	if p == "/" {
		return nil
	}
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func wsPathWithin(ws Workspace, root, child string) bool {
	rel, err := wsPathRel(ws, root, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	sep := string(os.PathSeparator)
	if !ws.Local() {
		sep = "/"
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+sep)
}

// nestedWorktreeFilter drops ripgrep results that live under a linked
// worktree nested below the search root. Ripgrep's glob language cannot
// express "this directory contains a linked worktree", so newNestedWorktreeFilter
// discovers the set of nested roots in one pass (ripgrep listing the .git
// files whose gitdir points under .git/worktrees) and excluded() then checks a
// result's ancestors in memory against that set without a per-directory Lstat.
type nestedWorktreeFilter struct {
	ws    Workspace
	root  string
	roots map[string]bool
}

func newNestedWorktreeFilter(ctx context.Context, ws Workspace, root string) (*nestedWorktreeFilter, error) {
	f := &nestedWorktreeFilter{ws: ws, root: wsPathClean(ws, root), roots: make(map[string]bool)}
	if !rgAvailable() {
		return f, nil
	}
	// -l -e worktrees lists only the .git files whose gitdir points under
	// .git/worktrees; a submodule's .git points under .git/modules and is
	// correctly left out. --hidden reveals the .git file itself (a dotfile);
	// !**/.*/** keeps the pass from descending into hidden directories, which
	// the search skips anyway.
	res, err := ws.Exec(ctx, ExecRequest{Command: "rg", Args: []string{
		"--hidden", "--no-messages", "-l", "-e", "worktrees",
		"--glob", "**/.git", "--glob", "!**/.*/**", "--", root,
	}})
	if res.ExitCode == 1 {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("rg worktree discovery exited %d", res.ExitCode)
	}
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		p := line
		if !wsPathIsAbs(ws, p) {
			if resolved, rerr := ws.ResolvePath(p); rerr == nil {
				p = resolved
			}
		}
		parent := wsPathClean(ws, wsPathDir(ws, p))
		if !wsPathWithin(ws, f.root, parent) || parent == f.root {
			continue
		}
		f.roots[parent] = true
	}
	return f, nil
}

func (f *nestedWorktreeFilter) excluded(file string) bool {
	if len(f.roots) == 0 {
		return false
	}
	p := file
	if !wsPathIsAbs(f.ws, p) {
		if resolved, err := f.ws.ResolvePath(p); err == nil {
			p = resolved
		}
	}
	dir := wsPathClean(f.ws, wsPathDir(f.ws, p))
	for {
		if dir == f.root {
			return false
		}
		if f.roots[dir] {
			return true
		}
		if !wsPathWithin(f.ws, f.root, dir) {
			return false
		}
		parent := wsPathDir(f.ws, dir)
		if parent == dir {
			return false
		}
		dir = wsPathClean(f.ws, parent)
	}
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
