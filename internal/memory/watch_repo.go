package memory

import (
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
}

// SaveSessionWatch inserts or replaces one watch row.
func (d *DB) SaveSessionWatch(sessionID string, w SessionWatch) error {
	if sessionID == "" {
		return fmt.Errorf("save session watch: session id is required")
	}
	_, err := d.sql.Exec(`
		INSERT INTO session_watches
			(session_id, id, on_spec, until_spec, state, evidence, armed_at, expires_at, fired_at, interval_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, id) DO UPDATE SET
			on_spec = excluded.on_spec,
			until_spec = excluded.until_spec,
			state = excluded.state,
			evidence = excluded.evidence,
			armed_at = excluded.armed_at,
			expires_at = excluded.expires_at,
			fired_at = excluded.fired_at,
			interval_ms = excluded.interval_ms`,
		sessionID, w.ID, w.On, w.Until, w.State, w.Evidence, w.ArmedAt, w.ExpiresAt, w.FiredAt, w.IntervalMs)
	if err != nil {
		return fmt.Errorf("save session watch %s: %w", w.ID, err)
	}
	return nil
}

// ListSessionWatches returns every persisted watch for a session, oldest
// first. The caller owns the slice.
func (d *DB) ListSessionWatches(sessionID string) ([]SessionWatch, error) {
	rows, err := d.sql.Query(`
		SELECT id, on_spec, until_spec, state, evidence, armed_at, expires_at, fired_at, interval_ms
		FROM session_watches
		WHERE session_id = ?
		ORDER BY armed_at, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list session watches: %w", err)
	}
	defer rows.Close()

	var out []SessionWatch
	for rows.Next() {
		var w SessionWatch
		if err := rows.Scan(&w.ID, &w.On, &w.Until, &w.State, &w.Evidence, &w.ArmedAt, &w.ExpiresAt, &w.FiredAt, &w.IntervalMs); err != nil {
			return nil, fmt.Errorf("scan session watch: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session watches: %w", err)
	}
	return out, nil
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
