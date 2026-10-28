package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/buchenberg/yaah/internal/config"
)

// DispatchGrants is a dispatch world's declared write authority, resolved
// against the world root. A zero Declared value means the legacy single
// writable root: the world itself.
type DispatchGrants struct {
	Writable []string
	ReadOnly []string
	Declared bool
}

// Restrictive reports whether the set narrows authority below the whole world.
// Only a restrictive set needs a jail: an advisory workspace cannot enforce it.
func (g DispatchGrants) Restrictive() bool { return g.Declared }

// ResolveGrants lowers the configured grants to absolute roots under worldRoot.
// Relative paths are joined to the world; absolute paths and anything that
// escapes the world are refused (validation already rejects them, but the
// seam refuses again rather than trusting a caller).
func ResolveGrants(grants []config.GrantConfig, worldRoot string) (DispatchGrants, error) {
	if len(grants) == 0 {
		return DispatchGrants{Writable: []string{worldRoot}}, nil
	}
	root, err := filepath.Abs(worldRoot)
	if err != nil {
		return DispatchGrants{}, fmt.Errorf("grants: resolve world root: %w", err)
	}

	g := DispatchGrants{Declared: true}
	for _, grant := range grants {
		rel := filepath.FromSlash(strings.TrimSpace(grant.Path))
		abs := filepath.Join(root, rel)
		if err := withinWorld(root, abs); err != nil {
			return DispatchGrants{}, fmt.Errorf("grants: %s: %w", grant.Path, err)
		}
		switch strings.ToLower(strings.TrimSpace(grant.Access)) {
		case "readwrite":
			g.Writable = append(g.Writable, abs)
		case "readonly":
			g.ReadOnly = append(g.ReadOnly, abs)
		default:
			return DispatchGrants{}, fmt.Errorf("grants: %s: unknown access %q", grant.Path, grant.Access)
		}
	}
	return g, nil
}

// withinWorld reports whether p is world root or beneath it.
func withinWorld(root, p string) error {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return err
	}
	if rel == "." {
		return fmt.Errorf("a grant may not name the world root; omit grants for whole-world access")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("escapes the dispatch world")
	}
	return nil
}
