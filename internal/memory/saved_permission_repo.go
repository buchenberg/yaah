package memory

import (
	"errors"
	"fmt"
	"time"
)

// SavedPermission is one durable "always" answer from an approval prompt: the
// user allowed this (tool, resource) shape once and chose always.
//
// It is project-scoped — ProjectID is the canonical workspace root, so an
// allow saved in one repository never widens a call in another — and consults
// as a Rule with Effect Allow, meaning the regular last-match-wins ruleset
// fold applies when the config ruleset names the same (tool, resource) pair:
// the configured ruleset wins on overlap.
type SavedPermission struct {
	// ProjectID is the canonical workspace root the decision was made in.
	ProjectID string
	// Action is the tool name the ask carried.
	Action string
	// Resource is the ask's resource derivation (glob, permissions
	// vocabulary); matching is on the derived resource, not on a pattern.
	Resource string
	// CreatedAt is when the user granted it.
	CreatedAt int64
}

// savedPermissionTable is the additive v5 table backing saved approvals. It
// lives in the base schema (migrate.go) — declared here so the repo methods
// and the DDL name one constant, not two string literals.
const savedPermissionTable = `CREATE TABLE IF NOT EXISTS saved_permissions (
	project_id TEXT NOT NULL,
	action     TEXT NOT NULL,
	resource   TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	UNIQUE (project_id, action, resource)
);`

// SaveSavedPermission records one allow decision. Rows are unique per
// (project, action, resource): re-saving a decision the user has already
// made is a no-op, not a second row.
func (d *DB) SaveSavedPermission(p SavedPermission) error {
	if p.ProjectID == "" {
		return fmt.Errorf("saved permission: empty project")
	}
	if p.Action == "" {
		return fmt.Errorf("saved permission: empty action")
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = time.Now().Unix()
	}
	if _, err := d.sql.Exec(`INSERT INTO saved_permissions
		(project_id, action, resource, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (project_id, action, resource) DO NOTHING`,
		p.ProjectID, p.Action, p.Resource, p.CreatedAt); err != nil {
		return fmt.Errorf("save saved permission: %w", err)
	}
	return nil
}

// SavedPermissions returns the project's saved allows as ruleset rows. Each
// row is one Effect Allow rule keyed on the tool name; a session's saved
// answers therefore participate in the ordinary last-match-wins ruleset
// fold, where the configured ruleset (evaluated after them) can still narrow
// or deny.
func (d *DB) SavedPermissions(projectID string) ([]SavedPermission, error) {
	rows, err := d.sql.Query(`SELECT project_id, action, resource, created_at
		FROM saved_permissions WHERE project_id = ?
		ORDER BY created_at DESC, action, resource`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list saved permissions: %w", err)
	}
	defer rows.Close()

	var out []SavedPermission
	for rows.Next() {
		var p SavedPermission
		if err := rows.Scan(&p.ProjectID, &p.Action, &p.Resource, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan saved permission: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ErrSavedPermissionNotFound is the sentinel wrapped (with the offending
// triple) by DeleteSavedPermission when the row does not exist, so a caller
// can separate a stale delete from a genuine database failure with errors.Is.
var ErrSavedPermissionNotFound = errors.New("saved permission not found")

// DeleteSavedPermission removes one row; deleting an unknown row wraps
// ErrSavedPermissionNotFound, never a silent no-op.
func (d *DB) DeleteSavedPermission(projectID, action, resource string) error {
	res, err := d.sql.Exec(`DELETE FROM saved_permissions
		WHERE project_id = ? AND action = ? AND resource = ?`,
		projectID, action, resource)
	if err != nil {
		return fmt.Errorf("delete saved permission: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("delete saved permission %q/%q/%q: %w", projectID, action, resource, ErrSavedPermissionNotFound)
	}
	return nil
}
