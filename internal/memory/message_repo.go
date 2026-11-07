package memory

import (
	"context"
	"database/sql"
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
			title = DeriveSessionTitle(m.Content)
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
			return
		}
		if err := d.mirrorMessageVec(id, emb); err != nil {
			slog.Warn("memory: mirror messages vec0 failed", "op", "embed_message", "id", id, "err", err)
		}
	}()
	return done
}

// MaxMessageIdx returns the highest persisted message index for a session,
// or -1 when the session has no rows. It is the recovery anchor for a
// persister whose cursor fell behind the store (an interrupted or
// overlapping run): appending at max+1 keeps the transcript contiguous
// instead of retrying an occupied position forever.
func (d *DB) MaxMessageIdx(sessionID string) (int, error) {
	var idx sql.NullInt64
	if err := d.sql.QueryRow(`SELECT MAX(idx) FROM messages WHERE session_id = ?`, sessionID).Scan(&idx); err != nil {
		return -1, err
	}
	if !idx.Valid {
		return -1, nil
	}
	return int(idx.Int64), nil
}

// DeleteMessagesFrom removes persisted message rows for a session at
// idx >= fromIdx. Used when the in-memory conversation is replaced
// wholesale (compaction rebase) or rewound (turn restore), so stale
// rows cannot conflict with re-persisted messages or resurrect on
// resume (review finding B7). FTS5 rows follow via the messages
// table's delete triggers; the messages_vec rows are removed here in the
// same transaction so a reused rowid cannot resurface a stale vector.
func (d *DB) DeleteMessagesFrom(sessionID string, fromIdx int) error {
	vecActive := d.messagesVecPresent()

	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if vecActive {
		if _, err := tx.Exec(`DELETE FROM messages_vec WHERE rowid IN (SELECT rowid FROM messages WHERE session_id = ? AND idx >= ?)`, sessionID, fromIdx); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ? AND idx >= ?`, sessionID, fromIdx); err != nil {
		return err
	}
	return tx.Commit()
}

// GetMessages returns all messages for a session.
func (d *DB) GetMessages(sessionID string) ([]Message, error) {
	rows, err := d.sql.Query(`
		SELECT session_id, idx, role, content, COALESCE(reasoning_content, ''), COALESCE(reasoning_signature, ''), COALESCE(tool_name, ''), COALESCE(tool_call_id, ''), COALESCE(tool_calls, ''), ts, COALESCE(id, ''), COALESCE(trace_id, ''), COALESCE(turn_id, '')
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

// CountMessages reports how many messages the store holds across every
// session. It exists so a caller can tell "there are past transcripts to
// recall" from "there are session rows but nothing in them": a session is
// created at wiring time before any turn runs, so a run that produced no
// messages leaves a row that recalls nothing, and memory_search_sessions
// skips message-less sessions rather than listing them.
func (d *DB) CountMessages() (int, error) {
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count messages: %w", err)
	}
	return n, nil
}

// SearchMessages searches past session messages using FTS5.
func (d *DB) SearchMessages(query string, limit int) ([]Message, error) {
	safeQuery := sanitizeFTSQuery(query)
	rows, err := d.sql.Query(`
		SELECT m.session_id, m.idx, m.role, m.content, COALESCE(m.reasoning_content, ''), COALESCE(m.reasoning_signature, ''), COALESCE(m.tool_name, ''), COALESCE(m.tool_call_id, ''), COALESCE(m.tool_calls, ''), m.ts, COALESCE(m.id, ''), COALESCE(m.trace_id, ''), COALESCE(m.turn_id, '')
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

// MessageVectorResult is a session message matched by semantic search. It
// carries the originating session, role, and timestamp so a recall can point at
// the conversation it came from — the pre-vec0 path returned only the content,
// which left the session implicit.
type MessageVectorResult struct {
	ID        string
	SessionID string
	Role      string
	Content   string
	Timestamp int64
	Score     float32
}

// SearchMessagesVector embeds the query and returns the top-K semantically
// matched messages, degrading an embed failure to an empty result so the caller
// keeps its keyword path. Use SearchMessagesVectorDiag when the reason for the
// degradation must be reported.
func (d *DB) SearchMessagesVector(ctx context.Context, query string, limit int) ([]MessageVectorResult, error) {
	results, err := d.SearchMessagesVectorDiag(ctx, query, limit)
	if IsEmbedFailure(err) {
		return nil, nil
	}
	return results, err
}

// SearchMessagesVectorDiag is SearchMessagesVector with the reason for a
// degraded semantic path preserved: a non-nil error means the vector path
// produced nothing, either because the query could not be embedded (an embed
// failure, already recorded for the log and OTel counter; classify it with
// IsEmbedFailure) or the store query failed. A nil embedder is not an error —
// it reports (nil, nil) — so a caller that must name that state checks
// Embedder() first.
func (d *DB) SearchMessagesVectorDiag(ctx context.Context, query string, limit int) ([]MessageVectorResult, error) {
	embedder := d.Embedder()
	if embedder == nil {
		return nil, nil
	}
	qEmb, err := EmbedQuery(ctx, embedder, query)
	if err != nil {
		d.recordEmbedFailure("search_messages_vector", err)
		return nil, &embedError{err: err}
	}

	if d.messagesVecPresent() {
		results, err := d.searchMessagesVecIndex(ctx, qEmb, limit)
		if err == nil {
			return results, nil
		}
		// A stale index (wrong width) degrades to the scan.
		return d.searchMessagesVectorScan(ctx, qEmb, limit)
	}
	return d.searchMessagesVectorScan(ctx, qEmb, limit)
}

// searchMessagesVectorScan is the pre-vec0 brute-force path: it scans messages
// with non-null embeddings and scores by cosine similarity in Go.
func (d *DB) searchMessagesVectorScan(ctx context.Context, qEmb Embedding, limit int) ([]MessageVectorResult, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, session_id, role, content, ts, embedding
		FROM messages
		WHERE embedding IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		r    MessageVectorResult
		blob []byte
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.r.ID, &c.r.SessionID, &c.r.Role, &c.r.Content, &c.r.Timestamp, &c.blob); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var results []MessageVectorResult
	for _, c := range candidates {
		if len(c.blob) == 0 {
			continue
		}
		c.r.Score = CosineSimilarity(qEmb, DecodeEmbedding(c.blob))
		results = append(results, c.r)
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

// ReembedStaleMessageEmbeddings heals the messages_vec index from the BLOB
// column: it re-embeds rows whose stored width differs from the embedder (the
// message-side counterpart to ReembedStaleEmbeddings), and re-mirrors rows whose
// BLOB is the correct width but whose vec0 row is missing (a crash between the
// BLOB update and the mirror would otherwise leave them invisible to the
// index-preferred search). Returns the number of rows healed; a nil embedder is
// a no-op. An embed failure is surfaced like any other and does not stop the
// sweep.
func (d *DB) ReembedStaleMessageEmbeddings(ctx context.Context) (int, error) {
	embedder := d.Embedder()
	if embedder == nil {
		return 0, nil
	}
	probe, err := embedder.Embed(ctx, "dimension probe")
	if err != nil {
		d.recordEmbedFailure("reembed_message", err)
		return 0, fmt.Errorf("reembed stale messages: probe embedder: %w", err)
	}
	dim := len(probe)

	// id != '' mirrors the embed gate in embedMessageAsync: a message with no id
	// was never embedded, and messages.id is not unique, so an UPDATE keyed on
	// id = '' would fan out across every empty-id row.
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, content FROM messages WHERE embedding IS NOT NULL AND length(embedding) != ? AND id != ''`, 4*dim)
	if err != nil {
		return 0, fmt.Errorf("reembed stale messages: query: %w", err)
	}
	defer rows.Close()

	type staleRow struct{ id, content string }
	var stale []staleRow
	for rows.Next() {
		var r staleRow
		if err := rows.Scan(&r.id, &r.content); err != nil {
			return 0, err
		}
		stale = append(stale, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	reembedded := 0
	for _, r := range stale {
		emb, err := embedder.Embed(ctx, r.content)
		if err != nil {
			d.recordEmbedFailure("reembed_message", err)
			continue
		}
		if _, err := d.sql.Exec(`UPDATE messages SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), r.id); err != nil {
			slog.Warn("memory: store message embedding failed", "op", "reembed_message", "id", r.id, "err", err)
			continue
		}
		if err := d.mirrorMessageVec(r.id, emb); err != nil {
			slog.Warn("memory: mirror messages vec0 failed", "op", "reembed_message", "id", r.id, "err", err)
		}
		reembedded++
	}

	// Re-mirror rows whose BLOB is the correct width but which never landed a
	// vec0 row. A single INSERT...SELECT repairs them without re-embedding.
	remirrored := 0
	if d.messagesVecPresent() {
		res, err := d.sql.ExecContext(ctx, `
			INSERT INTO messages_vec(rowid, embedding)
			SELECT m.rowid, m.embedding FROM messages m
			WHERE m.embedding IS NOT NULL AND length(m.embedding) = ?
			  AND NOT EXISTS (SELECT 1 FROM messages_vec v WHERE v.rowid = m.rowid)`, 4*dim)
		if err != nil {
			return reembedded, fmt.Errorf("reembed stale messages: re-mirror missing: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			remirrored = int(n)
		}
	}

	return reembedded + remirrored, nil
}

// ReconcileMessageEmbeddings backfills embeddings for user/assistant message
// rows with a NULL embedding, then heals rows left at a stale width after a
// model swap. Returns the number of rows updated. An error from any single
// embed does not stop the reconciliation.
func (d *DB) ReconcileMessageEmbeddings(ctx context.Context) (int, error) {
	embedder := d.Embedder()
	if embedder == nil {
		return 0, nil
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, content FROM messages WHERE embedding IS NULL AND role IN ('user','assistant') AND content != '' AND id != ''`)
	if err != nil {
		return 0, fmt.Errorf("reconcile messages: query: %w", err)
	}
	defer rows.Close()

	type row struct{ id, content string }
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.content); err != nil {
			return 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	updated := 0
	for _, r := range pending {
		emb, err := embedder.Embed(ctx, r.content)
		if err != nil {
			d.recordEmbedFailure("reconcile_messages", err)
			continue
		}
		if _, err := d.sql.Exec(`UPDATE messages SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), r.id); err != nil {
			slog.Warn("memory: store message embedding failed", "op", "reconcile_messages", "id", r.id, "err", err)
			continue
		}
		if err := d.mirrorMessageVec(r.id, emb); err != nil {
			slog.Warn("memory: mirror messages vec0 failed", "op", "reconcile_messages", "id", r.id, "err", err)
		}
		updated++
	}

	// A model swap leaves rows at the old dimensionality; fold the repair in so
	// the stale population drains instead of persisting unsearchable.
	healed, err := d.ReembedStaleMessageEmbeddings(ctx)
	if err != nil {
		slog.Warn("memory: re-embed stale messages failed", "err", err)
	}
	return updated + healed, nil
}
