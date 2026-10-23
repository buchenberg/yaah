// Package cwdpath canonicalizes working directories so that every surface
// which writes or filters on a session cwd spells the same path the same
// way: the ACP session intake, the persisted session store, the engine-level
// session registry (internal/sessions), the `yaah sessions` CLI, and the web
// session picker.
//
// It is a leaf package on purpose — no dependency on the agent, the store, or
// any protocol package — so both internal/acp and internal/sessions can use it
// without coupling the two.
package cwdpath

import (
	"fmt"
	"path/filepath"
)

// Canonical validates and canonicalizes a working directory that is used as a
// filter: the empty string is allowed and means "no filter" (matches every
// cwd), anything else must be absolute, and the result is cleaned with
// symlinks resolved.
//
// A directory that does not exist is NOT an error: a filter naming a directory
// the agent has never seen simply matches nothing. That tolerance is
// load-bearing — it makes the function idempotent on its own output and safe
// to apply twice (the ACP server canonicalizes a filter on intake and the
// registry canonicalizes it again), including for rows whose directory has
// since been deleted.
func Canonical(cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}
	if !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("cwd %q must be an absolute path", cwd)
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("cwd %q: %w", cwd, err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs), nil
}

// CanonicalStored is the best-effort variant for a cwd that is about to be
// persisted: it canonicalizes exactly like Canonical, but an unusable path is
// stored as given instead of failing session creation. Stored rows and the
// filters that read them stay in agreement because both sides go through
// Canonical.
func CanonicalStored(cwd string) string {
	if canonical, err := Canonical(cwd); err == nil {
		return canonical
	}
	return cwd
}
