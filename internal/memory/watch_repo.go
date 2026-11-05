package memory

import (
	"context"
	"database/sql"
	"fmt"
)

// SessionWatch is the persisted form of one armed watch (internal/wake). It
// lives in session_watches, keyed by (session_id, id), so each session owns
// its own armed set and a resume can rebuild it without seeing another
// session's watches.
type SessionWatch struct {
	ID         string
	On         string
	Until      string
	State      string
	Evidence   string
	ArmedAt    int64
	ExpiresAt  int64
	FiredAt    int64
	IntervalMs int64
	Owner      string
	LeaseUntil int64
}

// SaveSessionWatch inserts or replaces one watch row.
func (d *DB) SaveSessionWatch(sessionID string, w SessionWatch) error {
	if sessionID == "" {
		return fmt.Errorf("save session watch: session id is required")
	}
	_, err := d.sql.Exec(`
		INSERT INTO session_watches
			(session_id, id, on_spec, until_spec, state, evidence, armed_at, expires_at, fired_at, interval_ms, owner, lease_until)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, id) DO UPDATE SET
			on_spec = excluded.on_spec,
			until_spec = excluded.until_spec,
			state = excluded.state,
			evidence = excluded.evidence,
			armed_at = excluded.armed_at,
			expires_at = excluded.expires_at,
			fired_at = excluded.fired_at,
			interval_ms = excluded.interval_ms,
			owner = excluded.owner,
			lease_until = excluded.lease_until`,
		sessionID, w.ID, w.On, w.Until, w.State, w.Evidence, w.ArmedAt, w.ExpiresAt, w.FiredAt, w.IntervalMs, w.Owner, w.LeaseUntil)
	if err != nil {
		return fmt.Errorf("save session watch %s: %w", w.ID, err)
	}
	return nil
}

// ListSessionWatches returns every persisted watch for a session, oldest
// first. The caller owns the slice.
func (d *DB) ListSessionWatches(sessionID string) ([]SessionWatch, error) {
	rows, err := d.sql.Query(`
		SELECT id, on_spec, until_spec, state, evidence, armed_at, expires_at, fired_at, interval_ms, owner, lease_until
		FROM session_watches
		WHERE session_id = ?
		ORDER BY armed_at, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list session watches: %w", err)
	}
	defer rows.Close()
	return scanSessionWatches(rows)
}

// ClaimSessionWatches makes owner the owner of every armed watch for the
// session and returns the claimed set. Last resume wins: a newer process
// takes over and the older process's eventual fires are rejected by
// ClaimSessionWatchFire. Fired rows are deliberately not claimed: a live
// owner may still be delivering them, and a resume re-delivers them only
// after the previous lease expires. The UPDATE and SELECT share a
// transaction so the returned set is the one this owner claimed.
func (d *DB) ClaimSessionWatches(sessionID, owner string, leaseUntil int64) ([]SessionWatch, error) {
	tx, err := d.sql.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("begin watch claim: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE session_watches SET owner = ?, lease_until = ? WHERE session_id = ? AND state = 'armed'`,
		owner, leaseUntil, sessionID); err != nil {
		return nil, fmt.Errorf("claim session watches: %w", err)
	}
	rows, err := tx.Query(`
		SELECT id, on_spec, until_spec, state, evidence, armed_at, expires_at, fired_at, interval_ms, owner, lease_until
		FROM session_watches
		WHERE session_id = ? AND state = 'armed'
		ORDER BY armed_at, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list claimed watches: %w", err)
	}
	out, err := scanSessionWatches(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit watch claim: %w", err)
	}
	return out, nil
}

// ClaimSessionWatchFire transitions an armed watch owned by owner to fired.
// armedAt scopes the claim to that watch generation, so a same-id replacement
// is never marked. It reports false when another process owns the row, the
// row is no longer armed, or the generation changed, so a process that lost
// the last-resume race cannot deliver a wake the current owner is responsible
// for.
func (d *DB) ClaimSessionWatchFire(sessionID, id, owner string, armedAt, firedAt, leaseUntil int64, evidence string) (bool, error) {
	res, err := d.sql.Exec(`
		UPDATE session_watches
		SET state = 'fired', fired_at = ?, evidence = ?, owner = ?, lease_until = ?
		WHERE session_id = ? AND id = ? AND owner = ? AND state = 'armed' AND armed_at = ?`,
		firedAt, evidence, owner, leaseUntil, sessionID, id, owner, armedAt)
	if err != nil {
		return false, fmt.Errorf("claim watch fire %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim watch fire %s: %w", id, err)
	}
	return n > 0, nil
}

// DeleteSessionWatch removes one watch row. An unknown id is a no-op.
func (d *DB) DeleteSessionWatch(sessionID, id string) error {
	if _, err := d.sql.Exec(
		`DELETE FROM session_watches WHERE session_id = ? AND id = ?`, sessionID, id); err != nil {
		return fmt.Errorf("delete session watch %s: %w", id, err)
	}
	return nil
}

// DeleteSessionWatches removes every watch row for a session. It is the
// cleanup path for a session whose watches can no longer be honoured (the
// session was deleted or its workspace is gone), never the resume path.
func (d *DB) DeleteSessionWatches(sessionID string) error {
	if _, err := d.sql.Exec(`DELETE FROM session_watches WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete session watches: %w", err)
	}
	return nil
}

func scanSessionWatches(rows *sql.Rows) ([]SessionWatch, error) {
	var out []SessionWatch
	for rows.Next() {
		var w SessionWatch
		if err := rows.Scan(&w.ID, &w.On, &w.Until, &w.State, &w.Evidence, &w.ArmedAt, &w.ExpiresAt, &w.FiredAt, &w.IntervalMs, &w.Owner, &w.LeaseUntil); err != nil {
			return nil, fmt.Errorf("scan session watch: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session watches: %w", err)
	}
	return out, nil
}

// countSessionWatches is a test seam: the repo tests assert row ownership
// without reaching into session_watches' shape.
func (d *DB) countSessionWatches(sessionID string) (int, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM session_watches WHERE session_id = ?`, sessionID).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	return n, nil
}
