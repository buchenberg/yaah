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

	// DenyPatterns are filepath.Match globs matched against the base name
	// of the resolved path.  Useful for blocking sensitive files that might
	// exist inside the workspace (e.g. ".env", "*.pem").
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
	if strings.HasPrefix(input, "~") {
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

	// ── 3. Symlink resolution (best-effort) ──
	realPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Fall back to the unresolved absolute path — we still do the
		// containment check below.
		realPath = abs
	}

	// ── 4. Workspace containment ──
	if pv.WorkspaceRoot != "" {
		// Canonicalise the root one more time in case it changed since
		// construction.  Fast path if already canonicalised.
		root := pv.WorkspaceRoot

		rel, err := filepath.Rel(root, realPath)
		if err != nil {
			return "", fmt.Errorf("cannot compute relative path: %w", err)
		}
		if (strings.HasPrefix(rel, "..") || filepath.IsAbs(rel)) &&
			!pv.ask(realPath, fmt.Sprintf("outside the workspace (%s)", root)) {
			return "", fmt.Errorf("path %q is outside the workspace (%s)", realPath, root)
		}
	}

	// ── 5. Deny patterns (basename match) ──
	base := filepath.Base(realPath)
	for _, pattern := range pv.DenyPatterns {
		if matched, _ := filepath.Match(pattern, base); matched {
			if pv.ask("deny:"+realPath, fmt.Sprintf("matches deny pattern %q", pattern)) {
				continue
			}
			return "", fmt.Errorf("access to %q files is forbidden", base)
		}
	}

	return realPath, nil
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
	if pv.approved[key] {
		pv.mu.Unlock()
		return true
	}
	pv.mu.Unlock()

	if !pv.AskFn(path, reason) {
		return false
	}

	pv.mu.Lock()
	if pv.approved == nil {
		pv.approved = make(map[string]bool)
	}
	pv.approved[key] = true
	pv.mu.Unlock()
	return true
}

// PathValidatorSetter is implemented by file-accessing tools that need
// workspace containment.  The Registry calls SetPathValidator during
// Register so tools built via factories also receive the validator.
type PathValidatorSetter interface {
	SetPathValidator(pv *PathValidator)
}
