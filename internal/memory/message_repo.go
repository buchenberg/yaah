package memory

// AddMessage inserts a message into a session.
func (d *DB) AddMessage(m Message) error {
	_, err := d.sql.Exec(
		`INSERT INTO messages (session_id, idx, role, content, reasoning_content, tool_name, tool_call_id, tool_calls, ts, id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.SessionID, m.Idx, m.Role, m.Content, m.ReasoningContent, m.ToolName, m.ToolCallID, m.ToolCalls, m.Timestamp, m.ID,
	)
	return err
}

// GetMessages returns all messages for a session.
func (d *DB) GetMessages(sessionID string) ([]Message, error) {
	rows, err := d.sql.Query(`
		SELECT session_id, idx, role, content, COALESCE(reasoning_content, ''), tool_name, COALESCE(tool_call_id, ''), tool_calls, ts, COALESCE(id, '')
		FROM messages
		WHERE session_id = ?
		ORDER BY idx
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.SessionID, &m.Idx, &m.Role, &m.Content, &m.ReasoningContent, &m.ToolName, &m.ToolCallID, &m.ToolCalls, &m.Timestamp, &m.ID); err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, rows.Err()
}

// SearchMessages searches past session messages using FTS5.
func (d *DB) SearchMessages(query string, limit int) ([]Message, error) {
	safeQuery := sanitizeFTSQuery(query)
	rows, err := d.sql.Query(`
		SELECT m.session_id, m.idx, m.role, m.content, COALESCE(m.reasoning_content, ''), m.tool_name, COALESCE(m.tool_call_id, ''), m.tool_calls, m.ts, COALESCE(m.id, '')
		FROM messages m
		JOIN messages_fts ON messages_fts.rowid = m.rowid
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, safeQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.SessionID, &m.Idx, &m.Role, &m.Content, &m.ReasoningContent, &m.ToolName, &m.ToolCallID, &m.ToolCalls, &m.Timestamp, &m.ID); err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, rows.Err()
}
