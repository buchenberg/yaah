package memory

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// memoryDigest returns the exact content digest for a memory entry's text.
// Used for exact dedup; a plain SHA-256 suffices because memory text is a
// plain string (no need for shepherd's canonical-JSON key sorting).
func memoryDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// NewEntryID returns a fresh memory entry ID. IDs were previously
// mem-<UnixNano>, which collides when two entries are created within the
// clock's nanosecond resolution — coarse clocks make that a primary-key
// collision for back-to-back inserts, not a theoretical one. The random form
// matches newTurnID's shape: 128 bits of crypto/rand, hex-encoded, with a
// time-based fallback only if the system entropy source is unavailable.
func NewEntryID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return fmt.Sprintf("mem-%d", time.Now().UnixNano())
	}
	return "mem-" + hex.EncodeToString(b[:])
}

// AddMemory inserts a new memory entry. The caller should separately call
// EmbedMemoryAsync to produce the embedding vector.
func (d *DB) AddMemory(e Entry) error {
	scope, err := NormalizeScope(e.Scope)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(
		`INSERT INTO memory (id, text, tags, source, scope, session_id, created_at, digest) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Text, e.Tags, e.Source, scope, nullableText(e.SessionID), e.CreatedAt, memoryDigest(e.Text),
	)
	return err
}

// nullableText maps the empty string to SQL NULL: a global atom (or any atom
// written by a caller with no session) carries no session link, and the
// pruning sweeps in hygiene.go must never match an empty string as if it
// named a session.
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// EmbedMemoryAsync embeds the text in a background goroutine and stores
// the result. Returns a channel that closes when the embed completes —
// or immediately, with nothing written, when no embedder is configured
// or the store is closing (a skipped embed is recovered by the reconcile
// pass). Callers that need to wait for the embedding (e.g. the
// memory_add tool) can read from the channel.
func (d *DB) EmbedMemoryAsync(id, text string) <-chan struct{} {
	embedder := d.Embedder()
	if embedder == nil {
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
		emb, err := embedder.Embed(embedCtx, text)
		if err != nil {
			d.recordEmbedFailure("embed_memory", err)
			return
		}
		if _, err := d.sql.Exec(`UPDATE memory SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), id); err != nil {
			slog.Warn("memory: store embedding failed", "op", "embed_memory", "id", id, "err", err)
		}
	}()
	return done
}

// AddMemoryDedup adds a memory entry, skipping if text is identical to an
// existing entry. Returns the ID of the duplicate if found, or empty string
// if the entry was added. The lookup and insert run in a single write
// transaction (the DSN sets _txlock=immediate) so concurrent dedup calls
// cannot both observe "no match" and insert duplicate rows.
func (d *DB) AddMemoryDedup(e Entry) (string, error) {
	digest := memoryDigest(e.Text)

	tx, err := d.sql.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var dupID string
	err = tx.QueryRow(`SELECT id FROM memory WHERE digest = ? AND superseded_by IS NULL LIMIT 1`, digest).Scan(&dupID)
	if err == nil {
		return dupID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}

	scope, err := NormalizeScope(e.Scope)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO memory (id, text, tags, source, scope, session_id, created_at, digest) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Text, e.Tags, e.Source, scope, nullableText(e.SessionID), e.CreatedAt, digest,
	); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return "", nil
}

// GetMemory retrieves a single memory entry by ID.
func (d *DB) GetMemory(id string) (Entry, error) {
	row := d.sql.QueryRow(`
		SELECT id, text, tags, source, scope, COALESCE(session_id, ''), created_at, COALESCE(accessed_at, 0), access_count
		FROM memory WHERE id = ?
	`, id)
	var e Entry
	err := row.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.Scope, &e.SessionID, &e.CreatedAt, &e.AccessedAt, &e.AccessCount)
	return e, err
}

// ErrMemoryNotFound is the sentinel wrapped (with the offending ID) by
// DeleteMemory and UpdateMemory when no row matches, so consumers can
// separate a missing ID from a genuine database failure with errors.Is
// instead of substring-matching a message (#330 review follow-up).
var ErrMemoryNotFound = errors.New("memory not found")

// DeleteMemory removes a memory entry by ID. A delete matching no rows
// is an error, not a success: tool callers pass back whatever ID the
// tools surfaced, and a silent no-op delete let truncated IDs report
// "deleted" while the row — and its duplicates — accumulated (#328).
func (d *DB) DeleteMemory(id string) error {
	result, err := d.sql.Exec(`DELETE FROM memory WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrMemoryNotFound, id)
	}
	return nil
}

// UpdateMemory updates the text of an existing memory entry.
func (d *DB) UpdateMemory(id string, text string) error {
	result, err := d.sql.Exec(`UPDATE memory SET text = ?, digest = ? WHERE id = ?`, text, memoryDigest(text), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrMemoryNotFound, id)
	}
	return nil
}

// ListMemory returns the most recent memory entries.
func (d *DB) ListMemory(limit int) ([]Entry, error) {
	return d.ListMemoryScoped(limit, ScopeAll)
}

// CountMemories reports how many entries live in the named tier: ScopeGlobal,
// ScopeSession, or ScopeAll. It exists so a caller can say "there are memories
// here, just not in the tier you searched" instead of reporting a bare miss.
func (d *DB) CountMemories(scope string) (int, error) {
	scope, err := NormalizeScopeFilter(scope)
	if err != nil {
		return 0, err
	}
	countSQL := `SELECT COUNT(*) FROM memory WHERE superseded_by IS NULL`
	var args []any
	if scope != ScopeAll {
		countSQL += ` AND scope = ?`
		args = append(args, scope)
	}
	var n int
	if err := d.sql.QueryRow(countSQL, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count memory: %w", err)
	}
	return n, nil
}

// ListMemoryScoped returns the most recent memory entries, restricted to the
// named tier (ScopeGlobal, ScopeSession, or ScopeAll).
func (d *DB) ListMemoryScoped(limit int, scope string) ([]Entry, error) {
	scope, err := NormalizeScopeFilter(scope)
	if err != nil {
		return nil, err
	}

	listSQL := `
		SELECT id, text, tags, source, scope, COALESCE(session_id, ''), created_at, COALESCE(accessed_at, 0), access_count
		FROM memory
		WHERE superseded_by IS NULL`
	var listArgs []any
	if scope != ScopeAll {
		listSQL += ` AND scope = ?`
		listArgs = append(listArgs, scope)
	}
	listSQL += ` ORDER BY created_at DESC LIMIT ?`
	listArgs = append(listArgs, limit)

	rows, err := d.sql.Query(listSQL, listArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.Scope, &e.SessionID, &e.CreatedAt, &e.AccessedAt, &e.AccessCount); err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	return results, rows.Err()
}
