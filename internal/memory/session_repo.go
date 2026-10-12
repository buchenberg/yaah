package memory

// CreateSession inserts a new session. The system_prompt column is
// capped so an unbounded prompt cannot bloat the database (review A6/S7).
func (d *DB) CreateSession(s Session) error {
	_, err := d.sql.Exec(
		`INSERT INTO sessions (id, started_at, ended_at, cwd, model, tokens_in, tokens_out, system_prompt, workspace_backend, workspace_omitted_tools) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.StartedAt, s.EndedAt, s.CWD, s.Model, s.TokensIn, s.TokensOut, truncateWithMarker(s.SystemPrompt, maxSessionPromptBytes),
		s.WorkspaceBackend, s.WorkspaceOmittedTools,
	)
	return err
}

// UpdateSessionWorkspace records the workspace substrate a session ran
// under and the host-only tools isolated mode omitted (decision D4 of the
// isolated-workspace plan). Called after the tool registry is final, so
// the omitted list reflects what this session actually skipped. The
// columns describe the session's most recent run; a resumed session that
// changed mode overwrites them.
func (d *DB) UpdateSessionWorkspace(id, backend, omittedTools string) error {
	_, err := d.sql.Exec(
		`UPDATE sessions SET workspace_backend = ?, workspace_omitted_tools = ? WHERE id = ?`,
		backend, omittedTools, id,
	)
	return err
}

// GetSession retrieves a single session by ID.
func (d *DB) GetSession(id string) (Session, error) {
	row := d.sql.QueryRow(`
		SELECT id, started_at, COALESCE(ended_at, 0), cwd, model, tokens_in, tokens_out,
			COALESCE(tokens_cached, 0), COALESCE(sub_tokens_in, 0), COALESCE(sub_tokens_out, 0),
			COALESCE(system_prompt, ''), COALESCE(compacted_summary, ''),
			COALESCE(workspace_backend, ''), COALESCE(workspace_omitted_tools, '')
		FROM sessions WHERE id = ?
	`, id)
	var s Session
	err := row.Scan(&s.ID, &s.StartedAt, &s.EndedAt, &s.CWD, &s.Model, &s.TokensIn, &s.TokensOut,
		&s.TokensCached, &s.SubTokensIn, &s.SubTokensOut,
		&s.SystemPrompt, &s.CompactedSummary, &s.WorkspaceBackend, &s.WorkspaceOmittedTools)
	return s, err
}

// ListSessions returns recent sessions.
func (d *DB) ListSessions(limit int) ([]Session, error) {
	rows, err := d.sql.Query(`
		SELECT id, started_at, COALESCE(ended_at, 0), cwd, model, tokens_in, tokens_out,
			COALESCE(tokens_cached, 0), COALESCE(sub_tokens_in, 0), COALESCE(sub_tokens_out, 0),
			COALESCE(system_prompt, ''), COALESCE(compacted_summary, ''),
			COALESCE(workspace_backend, ''), COALESCE(workspace_omitted_tools, '')
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
		if err := rows.Scan(&s.ID, &s.StartedAt, &s.EndedAt, &s.CWD, &s.Model, &s.TokensIn, &s.TokensOut,
			&s.TokensCached, &s.SubTokensIn, &s.SubTokensOut,
			&s.SystemPrompt, &s.CompactedSummary, &s.WorkspaceBackend, &s.WorkspaceOmittedTools); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// EndSession sets the ended_at timestamp and final token counts for a
// session, including the cache-hit and sub-agent attribution columns.
func (d *DB) EndSession(id string, endedAt int64, tokensIn int, tokensOut int, tokensCached, subTokensIn, subTokensOut int) error {
	_, err := d.sql.Exec(
		`UPDATE sessions SET ended_at = ?, tokens_in = ?, tokens_out = ?, tokens_cached = ?, sub_tokens_in = ?, sub_tokens_out = ? WHERE id = ?`,
		endedAt, tokensIn, tokensOut, tokensCached, subTokensIn, subTokensOut, id)
	return err
}

// UpdateSessionUsage flushes the running usage counters without ending
// the session. Called incrementally (per LLM call) so a session killed
// mid-run — a harness timeout, a crash — still reports what it spent.
// ended_at is untouched: the session is still open.
func (d *DB) UpdateSessionUsage(id string, tokensIn int, tokensOut int, tokensCached, subTokensIn, subTokensOut int) error {
	_, err := d.sql.Exec(
		`UPDATE sessions SET tokens_in = ?, tokens_out = ?, tokens_cached = ?, sub_tokens_in = ?, sub_tokens_out = ? WHERE id = ?`,
		tokensIn, tokensOut, tokensCached, subTokensIn, subTokensOut, id)
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
