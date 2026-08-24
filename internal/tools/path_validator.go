package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PathValidator enforces workspace containment for file-accessing tools.
//
// A nil *PathValidator is safe — callers should check before calling ResolvePath
// and fall back to the legacy resolvePath behaviour.
type PathValidator struct {
	// WorkspaceRoot is the absolute, symlink-resolved root all file access
	// must stay within. Empty means "no restriction" (open access).
	WorkspaceRoot string

	// AllowHomeAccess enables ~ expansion.  When false, paths starting with
	// ~ are rejected outright.
	AllowHomeAccess bool

	// DenyPatterns are filepath.Match globs matched against the base
	// name of the resolved path and, when the path is inside the
	// workspace, against its slash-separated workspace-relative path
	// (so "config/*.secret" is expressible). Useful for blocking
	// sensitive files that might exist inside the workspace
	// (e.g. ".env", "*.pem").
	DenyPatterns []string

	// AskFn, when non-nil, converts a hard rejection into an interactive
	// approval prompt. It receives the offending path and a short reason
	// ("outside the workspace", deny pattern, home access); returning true
	// grants access for that path. A nil AskFn keeps hard-reject behaviour.
	AskFn func(path, reason string) bool

	// mu guards approved.
	mu sync.Mutex
	// approved remembers granted exceptions keyed by path + reason class
	// so retries within a session do not re-prompt for the same access.
	approved map[string]bool
}

// NewPathValidator returns a PathValidator ready for use.
func NewPathValidator(workspaceRoot string, allowHome bool, denyPatterns []string) *PathValidator {
	// Resolve the workspace root up-front so every ResolvePath call is fast.
	if workspaceRoot != "" {
		abs, err := filepath.Abs(workspaceRoot)
		if err == nil {
			workspaceRoot = abs
		}
		if real, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
			workspaceRoot = real
		}
	}
	return &PathValidator{
		WorkspaceRoot:   workspaceRoot,
		AllowHomeAccess: allowHome,
		DenyPatterns:    denyPatterns,
	}
}

// ResolvePath validates and resolves a user-supplied file path.
//
// Steps (in order):
//  1. Expand ~ (if allowed) or reject it.
//  2. Convert to absolute path.
//  3. Resolve symlinks.
//  4. Check containment within WorkspaceRoot (if set).
//  5. Check deny patterns.
//
// Returns the resolved absolute path or an error suitable for display to
// the model.
func (pv *PathValidator) ResolvePath(input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("empty path")
	}

	// ── 1. Home-directory expansion ──
	if input == "~" || strings.HasPrefix(input, "~/") {
		if !pv.AllowHomeAccess && !pv.ask("home:"+input, "home-directory access is not allowed in this session") {
			return "", fmt.Errorf("home-directory access is not allowed in this session (use an absolute or relative path inside the workspace)")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home directory: %w", err)
		}
		input = filepath.Join(home, input[1:]) // handles ~/foo and ~ alone
	}

	// ── 2. Absolute path ──
	abs, err := filepath.Abs(input)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path %q: %w", input, err)
	}

	// ── 3. Symlink resolution ──
	// Fails CLOSED: when no ancestor resolves, the path is rejected
	// rather than used unresolved (the old fallback let an unresolved
	// path through, which containment then validated against the
	// wrong tree). A not-yet-existing leaf is fine — its nearest
	// existing ancestor resolves and the tail is re-appended, which
	// also handles symlinks like macOS /var → /private/var.
	realPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved, resolveErr := resolveExistingAncestor(abs)
		if resolveErr != nil {
			return "", fmt.Errorf("cannot resolve path %q: %w", abs, resolveErr)
		}
		realPath = resolved
	}

	// ── 4. Workspace containment ──
	// relInWorkspace is the slash-separated workspace-relative path when
	// the resolved path is inside the workspace; empty otherwise. Deny
	// patterns use it for path-segment globs (step 5).
	var relInWorkspace string
	if pv.WorkspaceRoot != "" {
		// Canonicalise the root one more time in case it changed since
		// construction.  Fast path if already canonicalised.
		root := pv.WorkspaceRoot

		rel, err := filepath.Rel(root, realPath)
		if err != nil {
			return "", fmt.Errorf("cannot compute relative path: %w", err)
		}
		if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			if !pv.ask(realPath, fmt.Sprintf("outside the workspace (%s)", root)) {
				return "", fmt.Errorf("path %q is outside the workspace (%s)", realPath, root)
			}
		} else {
			relInWorkspace = filepath.ToSlash(rel)
		}
	}

	// ── 5. Deny patterns ──
	// Every pattern is tried against the basename and, when the path is
	// inside the workspace, against its workspace-relative path so both
	// "*.pem" and "config/*.secret" styles work.
	base := filepath.Base(realPath)
	for _, pattern := range pv.DenyPatterns {
		baseMatched, _ := filepath.Match(pattern, base)
		relMatched := false
		if relInWorkspace != "" {
			relMatched, _ = filepath.Match(pattern, relInWorkspace)
		}
		if baseMatched || relMatched {
			if pv.ask("deny:"+realPath, fmt.Sprintf("matches deny pattern %q", pattern)) {
				continue
			}
			return "", fmt.Errorf("access to %q is forbidden (deny pattern %q)", base, pattern)
		}
	}

	return realPath, nil
}

// resolveExistingAncestor resolves the deepest existing ancestor of abs
// via EvalSymlinks and re-appends the unresolved tail, so paths whose
// leaf does not exist yet still get their ancestor symlinks resolved.
// Returns an error when nothing along the path resolves — callers must
// reject such paths rather than use them unresolved.
func resolveExistingAncestor(abs string) (string, error) {
	cur := abs
	var tail []string
	for i := 0; i < 64; i++ {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			out := real
			for j := len(tail) - 1; j >= 0; j-- {
				out = filepath.Join(out, tail[j])
			}
			return out, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
	return "", fmt.Errorf("no existing path component resolves")
}

// ask consults AskFn for an exception to a rejection, caching grants so
// the same path+reason is only prompted once per session. It returns
// false (hard reject) when no AskFn is configured.
func (pv *PathValidator) ask(path, reason string) bool {
	if pv.AskFn == nil {
		return false
	}
	key := path + "\x00" + reason
	pv.mu.Lock()
	defer pv.mu.Unlock()
	if pv.approved[key] {
		return true
	}
	if !pv.AskFn(path, reason) {
		return false
	}
	if pv.approved == nil {
		pv.approved = make(map[string]bool)
	}
	pv.approved[key] = true
	return true
}

// PathValidatorSetter is implemented by file-accessing tools that need
// workspace containment.  The Registry calls SetPathValidator during
// Register so tools built via factories also receive the validator.
type PathValidatorSetter interface {
	SetPathValidator(pv *PathValidator)
}
