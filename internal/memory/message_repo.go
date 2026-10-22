package memory

import (
	"context"
	"fmt"
	"log/slog"
)

// AddMessage inserts a message into a session. When an embedder is
// configured and the role is "user" or "assistant", the content is
// embedded in a background goroutine so the caller is not blocked.
func (d *DB) AddMessage(m Message) error {
	res, err := d.sql.Exec(
		`INSERT INTO messages (session_id, idx, role, content, reasoning_content, reasoning_signature, tool_name, tool_call_id, tool_calls, ts, id, trace_id, turn_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, idx) DO NOTHING`,
		m.SessionID, m.Idx, m.Role, m.Content, m.ReasoningContent, m.ReasoningSignature, m.ToolName, m.ToolCallID, m.ToolCalls, m.Timestamp, m.ID, m.TraceID, m.TurnID,
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
		// Picker bookkeeping (title + last-activity timestamp) rides
		// the durable insert. It is best-effort: the message row is
		// already committed, and a stale timestamp only skews the
		// ordering in `session/list` — it must not fail a healthy
		// message write or confuse the agent loop's retry path.
		title := ""
		if m.Role == "user" {
			title = deriveSessionTitle(m.Content)
		}
		_ = d.TouchSession(m.SessionID, m.Timestamp, title)
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
// closes when the embed completes — or immediately, with nothing
// written, when no embedder is configured or the store is closing (a
// skipped embed is recovered by the reconcile pass).
func (d *DB) embedMessageAsync(id, role, content string) <-chan struct{} {
	embedder := d.Embedder()
	if embedder == nil || role != "user" && role != "assistant" || content == "" || id == "" {
		return nil
	}
	done := make(chan struct{})
	// Drain cancels the context, so a slow embedding server cannot keep
	// the write queued past a timed-out Close.
	embedCtx, cancel := context.WithCancel(context.Background())
	finish, ok := d.embeds.begin(cancel)
	if !ok {
		// Close has begun: skip the write rather than race the pool.
		cancel()
		close(done)
		return done
	}
	go func() {
		defer finish()
		defer close(done)
		defer cancel()
		emb, err := embedder.Embed(embedCtx, content)
		if err != nil {
			d.recordEmbedFailure("embed_message", err)
			return
		}
		if _, err := d.sql.Exec(`UPDATE messages SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), id); err != nil {
			slog.Warn("memory: store embedding failed", "op", "embed_message", "id", id, "err", err)
		}
	}()
	return done
}

// DeleteMessagesFrom removes persisted message rows for a session at
// idx >= fromIdx. Used when the in-memory conversation is replaced
// wholesale (compaction rebase) or rewound (turn restore), so stale
// rows cannot conflict with re-persisted messages or resurrect on
// resume (review finding B7). FTS5 rows follow via the messages
// table's delete triggers.
func (d *DB) DeleteMessagesFrom(sessionID string, fromIdx int) error {
	_, err := d.sql.Exec(`DELETE FROM messages WHERE session_id = ? AND idx >= ?`, sessionID, fromIdx)
	return err
}

// GetMessages returns all messages for a session.
func (d *DB) GetMessages(sessionID string) ([]Message, error) {
	rows, err := d.sql.Query(`
		SELECT session_id, idx, role, content, COALESCE(reasoning_content, ''), COALESCE(reasoning_signature, ''), tool_name, COALESCE(tool_call_id, ''), tool_calls, ts, COALESCE(id, ''), COALESCE(trace_id, ''), COALESCE(turn_id, '')
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
		if err := rows.Scan(&m.SessionID, &m.Idx, &m.Role, &m.Content, &m.ReasoningContent, &m.ReasoningSignature, &m.ToolName, &m.ToolCallID, &m.ToolCalls, &m.Timestamp, &m.ID, &m.TraceID, &m.TurnID); err != nil {
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
		SELECT m.session_id, m.idx, m.role, m.content, COALESCE(m.reasoning_content, ''), COALESCE(m.reasoning_signature, ''), m.tool_name, COALESCE(m.tool_call_id, ''), m.tool_calls, m.ts, COALESCE(m.id, ''), COALESCE(m.trace_id, ''), COALESCE(m.turn_id, '')
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
		if err := rows.Scan(&m.SessionID, &m.Idx, &m.Role, &m.Content, &m.ReasoningContent, &m.ReasoningSignature, &m.ToolName, &m.ToolCallID, &m.ToolCalls, &m.Timestamp, &m.ID, &m.TraceID, &m.TurnID); err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, rows.Err()
}

// SearchMessagesVector embeds the query, scans messages with non-null
// embeddings, and returns the top-K by cosine similarity. Falls back
// to empty when no embedder is configured; a failed embedding call
// degrades to empty and is surfaced via the rate-limited warning log
// and the OTel embed-failure counter.
func (d *DB) SearchMessagesVector(ctx context.Context, query string, limit int) ([]VectorResult, error) {
	embedder := d.Embedder()
	if embedder == nil {
		return nil, nil
	}
	qEmb, err := embedder.Embed(ctx, query)
	if err != nil {
		d.recordEmbedFailure("search_messages_vector", err)
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
