package memory

import (
	"context"
	"fmt"
)

// AddMessage inserts a message into a session. When an embedder is
// configured and the role is "user" or "assistant", the content is
// embedded in a background goroutine so the caller is not blocked.
func (d *DB) AddMessage(m Message) error {
	res, err := d.sql.Exec(
		`INSERT INTO messages (session_id, idx, role, content, reasoning_content, tool_name, tool_call_id, tool_calls, ts, id, trace_id, turn_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, idx) DO NOTHING`,
		m.SessionID, m.Idx, m.Role, m.Content, m.ReasoningContent, m.ToolName, m.ToolCallID, m.ToolCalls, m.Timestamp, m.ID, m.TraceID, m.TurnID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		d.embedMessageAsync(m.ID, m.Role, m.Content)
		return nil
	}

	// The position is already occupied. Treat it as an idempotent retry only
	// when the stored row carries the same content fingerprint (the
	// deterministic ID covers all immutable fields); otherwise it is a
	// different message and must not be silently dropped.
	var existingID string
	if err := d.sql.QueryRow(`SELECT id FROM messages WHERE session_id = ? AND idx = ?`, m.SessionID, m.Idx).Scan(&existingID); err != nil {
		return err
	}
	if existingID != m.ID {
		return fmt.Errorf("message conflict at (%s, %d): stored content differs", m.SessionID, m.Idx)
	}
	return nil
}

// embedMessageAsync embeds the content in a background goroutine and
// stores the result. Tool messages are skipped. Returns a channel that
// closes when the embed completes, or nil when no embedder is configured.
func (d *DB) embedMessageAsync(id, role, content string) <-chan struct{} {
	if d.embedder == nil || role != "user" && role != "assistant" || content == "" || id == "" {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		emb, err := d.embedder.Embed(context.Background(), content)
		if err != nil {
			return
		}
		d.embMu.Lock()
		d.sql.Exec(`UPDATE messages SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), id)
		d.embMu.Unlock()
	}()
	return done
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

// SearchMessagesVector embeds the query, scans messages with non-null
// embeddings, and returns the top-K by cosine similarity. Falls back
// to empty when no embedder is configured.
func (d *DB) SearchMessagesVector(ctx context.Context, query string, limit int) ([]VectorResult, error) {
	if d.embedder == nil {
		return nil, nil
	}
	qEmb, err := d.embedder.Embed(ctx, query)
	if err != nil {
		return nil, nil
	}

	rows, err := d.sql.Query(`
		SELECT id, content, embedding
		FROM messages
		WHERE embedding IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		id      string
		content string
		blob    []byte
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.content, &c.blob); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var results []VectorResult
	for _, c := range candidates {
		if len(c.blob) == 0 {
			continue
		}
		emb := DecodeEmbedding(c.blob)
		score := CosineSimilarity(qEmb, emb)
		results = append(results, VectorResult{
			Entry: Entry{ID: c.id, Text: c.content},
			Score: score,
		})
	}

	// Sort descending.
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
