package memory

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"time"
)

// RecordTraceOwner persists the mapping from a Shepherd trace owner ID
// (the session ID for main agents, a synthetic `sub-{role}-{parent}-{ts}`
// ID for sub-agents) to the parent session that spawned it. This makes
// Shepherd facts joinable to `sessions` rows even when the owner differs
// from the session ID. Idempotent per owner.
func (d *DB) RecordTraceOwner(ownerID, parentSession, role string) error {
	_, err := d.sql.Exec(
		`INSERT INTO trace_owners (owner_id, parent_session, role, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(owner_id) DO NOTHING`,
		ownerID, parentSession, role, time.Now().Unix(),
	)
	return err
}

// TraceOwnerParent returns the parent session recorded for a trace owner,
// or "" when the owner is unknown. Main-agent owners map to themselves.
func (d *DB) TraceOwnerParent(ownerID string) (string, error) {
	var parent string
	err := d.sql.QueryRow(`SELECT parent_session FROM trace_owners WHERE owner_id = ?`, ownerID).Scan(&parent)
	if err != nil {
		return "", err // sql.ErrNoRows when unknown
	}
	return parent, nil
}

// TraceOwnerParentsReadOnly resolves trace owners without opening the mutable
// database path: reports must not create or migrate state.db. A missing database
// is an empty mapping, not an error.
func TraceOwnerParentsReadOnly(path string, ownerIDs []string) (map[string]string, error) {
	if len(ownerIDs) == 0 {
		return nil, nil
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := make(map[string]string, len(ownerIDs))
	for _, owner := range ownerIDs {
		var parent string
		err := db.QueryRow(`SELECT parent_session FROM trace_owners WHERE owner_id = ?`, owner).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if parent != "" {
			out[owner] = parent
		}
	}
	return out, nil
}
