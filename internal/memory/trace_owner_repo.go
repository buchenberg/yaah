package memory

import "time"

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
