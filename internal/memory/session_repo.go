package memory

// CreateSession inserts a new session. The system_prompt column is
// capped so an unbounded prompt cannot bloat the database (review A6/S7).
func (d *DB) CreateSession(s Session) error {
	_, err := d.sql.Exec(
		`INSERT INTO sessions (id, started_at, ended_at, cwd, model, tokens_in, tokens_out, system_prompt) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.StartedAt, s.EndedAt, s.CWD, s.Model, s.TokensIn, s.TokensOut, truncateWithMarker(s.SystemPrompt, maxSessionPromptBytes),
	)
	return err
}

// GetSession retrieves a single session by ID.
func (d *DB) GetSession(id string) (Session, error) {
	row := d.sql.QueryRow(`
		SELECT id, started_at, COALESCE(ended_at, 0), cwd, model, tokens_in, tokens_out,
			COALESCE(system_prompt, ''), COALESCE(compacted_summary, '')
		FROM sessions WHERE id = ?
	`, id)
	var s Session
	err := row.Scan(&s.ID, &s.StartedAt, &s.EndedAt, &s.CWD, &s.Model, &s.TokensIn, &s.TokensOut,
		&s.SystemPrompt, &s.CompactedSummary)
	return s, err
}

// ListSessions returns recent sessions.
func (d *DB) ListSessions(limit int) ([]Session, error) {
	rows, err := d.sql.Query(`
		SELECT id, started_at, COALESCE(ended_at, 0), cwd, model, tokens_in, tokens_out,
			COALESCE(system_prompt, ''), COALESCE(compacted_summary, '')
		FROM sessions
		ORDER BY started_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.StartedAt, &s.EndedAt, &s.CWD, &s.Model, &s.TokensIn, &s.TokensOut, &s.SystemPrompt, &s.CompactedSummary); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// EndSession sets the ended_at timestamp and final token counts for a session.
func (d *DB) EndSession(id string, endedAt int64, tokensIn int, tokensOut int) error {
	_, err := d.sql.Exec(`UPDATE sessions SET ended_at = ?, tokens_in = ?, tokens_out = ? WHERE id = ?`, endedAt, tokensIn, tokensOut, id)
	return err
}

// UpdateSessionSummary persists the most recent compaction summary for
// a session, capped like system_prompt (review A6/S7).
func (d *DB) UpdateSessionSummary(id string, summary string) error {
	_, err := d.sql.Exec(`UPDATE sessions SET compacted_summary = ? WHERE id = ?`,
		truncateWithMarker(summary, maxSessionPromptBytes), id)
	return err
}

// GetCompactionCooldown returns the compaction cooldown state for a session.
func (d *DB) GetCompactionCooldown(sessionID string) (cooldownUntil int64, ineffective int, err error) {
	row := d.sql.QueryRow(
		`SELECT COALESCE(compaction_cooldown_until, 0), COALESCE(ineffective_compactions, 0) FROM sessions WHERE id = ?`,
		sessionID,
	)
	err = row.Scan(&cooldownUntil, &ineffective)
	if err != nil {
		return 0, 0, err
	}
	return
}

// SetCompactionCooldown persists the compaction cooldown state for a session.
func (d *DB) SetCompactionCooldown(sessionID string, cooldownUntil int64, ineffective int) error {
	_, err := d.sql.Exec(
		`UPDATE sessions SET compaction_cooldown_until = ?, ineffective_compactions = ? WHERE id = ?`,
		cooldownUntil, ineffective, sessionID,
	)
	return err
}
