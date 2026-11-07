package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	// Registers the vec0 virtual table module on every connection opened by
	// the modernc.org/sqlite driver (side effect via sqlite3_auto_extension).
	_ "modernc.org/sqlite/vec"
)

// memoryVecTable and messagesVecTable are the vec0 virtual tables backing
// semantic memory and session-message search respectively. They share one
// vector_dim stamp because both embed with the same model.
const (
	memoryVecTable   = "memory_vec"
	messagesVecTable = "messages_vec"
)

// vectorDimKey is the schema_meta key holding the embedding width the vec0
// index tables were created for. vec0 pins its dimension at CREATE, so the
// width must be reconciled against the configured embedder (see
// ensureVectorTables) — a model swap changes it.
const vectorDimKey = "vector_dim"

// memoryVecDDL is the vec0 table definition. embedding is the vector column;
// scope and superseded_by are filterable metadata columns. vec0 metadata
// columns reject NULL, so superseded_by stores ” for live rows (mirroring
// COALESCE(superseded_by, ”)) and the search filters with `= ”` rather than
// IS NULL.
const memoryVecDDL = `CREATE VIRTUAL TABLE IF NOT EXISTS ` + memoryVecTable + ` USING vec0(
	embedding float[%d] distance_metric=cosine,
	scope text,
	superseded_by text
);`

// messagesVecDDL is the vec0 table for session messages. It carries no
// metadata columns — messages have no scope/superseded tier — so the search
// joins back to messages for the content and filters nothing at the index.
const messagesVecDDL = `CREATE VIRTUAL TABLE IF NOT EXISTS ` + messagesVecTable + ` USING vec0(
	embedding float[%d] distance_metric=cosine
);`

// EnsureVectorIndex creates (or recreates) the vec0 index tables (memory_vec
// and messages_vec) at the width the configured embedder produces, and stamps
// that width. It is the startup reconciliation point: a model swap changes the
// width, so stale tables are dropped and recreated for the follow-up re-embed
// pass to repopulate.
//
// Idempotent and safe to call repeatedly. Returns recreated=true when any
// table was (re)created and needs repopulation. A nil embedder is a no-op; a
// probe failure (embedder unreachable) is returned so callers can degrade
// gracefully and retry via the write/reconcile paths.
func (d *DB) EnsureVectorIndex(ctx context.Context) (recreated bool, err error) {
	if d.Embedder() == nil {
		return false, nil
	}
	probe, err := d.Embedder().Embed(ctx, "dimension probe")
	if err != nil {
		return false, err
	}
	if len(probe) == 0 {
		return false, fmt.Errorf("vec0 index: embedder returned an empty vector")
	}
	return d.ensureVectorTables(len(probe))
}

// ensureVectorTables makes both vec0 index tables exist at exactly dim,
// recreating either when the current table was created at a different width.
// It serializes on vecMu because DDL (CREATE/DROP VIRTUAL TABLE) is a shared
// write and concurrent embed goroutines may reach it simultaneously.
func (d *DB) ensureVectorTables(dim int) (bool, error) {
	d.vecMu.Lock()
	defer d.vecMu.Unlock()

	stamped, hasStamp, err := d.vecDim()
	if err != nil {
		return false, err
	}
	recreated := false
	// ensure reconciles one table: no-op when it exists at the stamped width,
	// otherwise drop+create+backfill from its source BLOB column.
	ensure := func(name, ddl string, backfill func(int) error) error {
		exists, err := d.tableExists(name)
		if err != nil {
			return err
		}
		if exists && hasStamp && stamped == dim {
			return nil
		}
		if exists {
			if _, err := d.sql.Exec(`DROP TABLE IF EXISTS ` + name); err != nil {
				return fmt.Errorf("vec0 index: drop stale %s: %w", name, err)
			}
		}
		if _, err := d.sql.Exec(fmt.Sprintf(ddl, dim)); err != nil {
			return fmt.Errorf("vec0 index: create %s: %w", name, err)
		}
		// Populate the fresh (or recreated) table from rows already embedded
		// at this width, so an upgrade does not leave pre-existing rows
		// invisible to the new index. On a model swap the BLOBs are
		// stale-width, so this copies nothing and the re-embed sweep
		// repopulates instead.
		if err := backfill(dim); err != nil {
			return fmt.Errorf("vec0 index: backfill %s: %w", name, err)
		}
		recreated = true
		return nil
	}
	if err := ensure(memoryVecTable, memoryVecDDL, d.backfillMemoryVec); err != nil {
		return false, err
	}
	if err := ensure(messagesVecTable, messagesVecDDL, d.backfillMessagesVec); err != nil {
		return false, err
	}
	if err := d.setVecDim(dim); err != nil {
		return false, err
	}
	return recreated, nil
}

// backfillMemoryVec copies rows already embedded at dim from the memory BLOB
// column into memory_vec, so a newly created (or recreated) index starts
// consistent with the store rather than empty.
func (d *DB) backfillMemoryVec(dim int) error {
	_, err := d.sql.Exec(`
		INSERT INTO memory_vec(rowid, embedding, scope, superseded_by)
		SELECT rowid, embedding, scope, COALESCE(superseded_by, '')
		FROM memory
		WHERE embedding IS NOT NULL AND length(embedding) = ?`, 4*dim)
	return err
}

// backfillMessagesVec copies rows already embedded at dim from the messages
// BLOB column into messages_vec.
func (d *DB) backfillMessagesVec(dim int) error {
	_, err := d.sql.Exec(`
		INSERT INTO messages_vec(rowid, embedding)
		SELECT rowid, embedding
		FROM messages
		WHERE embedding IS NOT NULL AND length(embedding) = ?`, 4*dim)
	return err
}

// vecDim reads the stamped vector width from schema_meta.
func (d *DB) vecDim() (int, bool, error) {
	var v string
	err := d.sql.QueryRow(`SELECT value FROM schema_meta WHERE key = ?`, vectorDimKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false, fmt.Errorf("vec0 index: corrupt vector_dim stamp %q: %w", v, err)
	}
	return n, true, nil
}

func (d *DB) setVecDim(dim int) error {
	_, err := d.sql.Exec(
		`INSERT INTO schema_meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		vectorDimKey, strconv.Itoa(dim),
	)
	return err
}

// tableExists reports whether name is a table or virtual table in the schema.
func (d *DB) tableExists(name string) (bool, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

// vecIndexPresent reports whether the vec0 table currently exists. The search
// path uses it to choose the indexed path over the brute-force scan.
func (d *DB) vecIndexPresent() bool {
	ok, err := d.tableExists(memoryVecTable)
	return err == nil && ok
}

// messagesVecPresent reports whether the messages vec0 table currently exists.
func (d *DB) messagesVecPresent() bool {
	ok, err := d.tableExists(messagesVecTable)
	return err == nil && ok
}

// mirrorVec writes (or refreshes) the vec0 row for a memory id so the index
// mirrors the memory.embedding BLOB plus its scope and supersession state.
// The table is created lazily at the embedding's width, so the write path
// self-heals when the embedder only became reachable after startup.
func (d *DB) mirrorVec(id string, emb Embedding) error {
	if _, err := d.ensureVectorTables(len(emb)); err != nil {
		return err
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var rowid int64
	var scope, sup string
	err = tx.QueryRow(
		`SELECT rowid, scope, COALESCE(superseded_by, '') FROM memory WHERE id = ?`, id,
	).Scan(&rowid, &scope, &sup)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // deleted concurrently; nothing to mirror
	}
	if err != nil {
		return err
	}

	// DELETE + INSERT: vec0 upsert support is not assumed (spike verified plain
	// INSERT only), so a rowid-keyed replace is the safe fallback.
	if _, err := tx.Exec(`DELETE FROM memory_vec WHERE rowid = ?`, rowid); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO memory_vec(rowid, embedding, scope, superseded_by) VALUES (?, ?, ?, ?)`,
		rowid, EncodeEmbedding(emb), scope, sup,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteVecByMemoryID removes the vec0 row for a still-live memory id, via a
// rowid subquery. Used by supersede, where the memory row survives with its
// superseded_by marker set. Best-effort.
func (d *DB) deleteVecByMemoryID(id string) {
	if !d.vecIndexPresent() {
		return
	}
	if _, err := d.sql.Exec(`DELETE FROM memory_vec WHERE rowid = (SELECT rowid FROM memory WHERE id = ?)`, id); err != nil {
		slog.Warn("memory: delete vec0 row failed", "id", id, "err", err)
	}
}

// reindexVecByID rebuilds the vec0 row for a memory id from its stored BLOB
// and current metadata. Used after a promote, where only the scope metadata
// changed and the vector itself is unchanged. Best-effort.
func (d *DB) reindexVecByID(id string) {
	if !d.vecIndexPresent() {
		return
	}
	var rowid int64
	var blob []byte
	var scope, sup string
	err := d.sql.QueryRow(
		`SELECT rowid, COALESCE(embedding, ''), scope, COALESCE(superseded_by, '') FROM memory WHERE id = ?`, id,
	).Scan(&rowid, &blob, &scope, &sup)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("memory: reindex vec0 row failed", "id", id, "err", err)
		return
	}
	if len(blob) == 0 {
		return // not embedded yet; the reconcile pass will mirror it
	}
	if _, err := d.sql.Exec(`DELETE FROM memory_vec WHERE rowid = ?`, rowid); err != nil {
		slog.Warn("memory: reindex vec0 row delete failed", "id", id, "err", err)
		return
	}
	if _, err := d.sql.Exec(`INSERT INTO memory_vec(rowid, embedding, scope, superseded_by) VALUES (?, ?, ?, ?)`, rowid, blob, scope, sup); err != nil {
		slog.Warn("memory: reindex vec0 row insert failed", "id", id, "err", err)
	}
}

// searchVecIndex runs the vec0 nearest-neighbour search and joins back to the
// memory table for the Entry fields. The scope/superseded filters are applied
// as vec0 metadata predicates, so k counts only matching rows (the usual
// KNN-then-join-then-filter ordering would under-fill the page).
func (d *DB) searchVecIndex(ctx context.Context, qEmb Embedding, limit int, scope string) ([]VectorResult, error) {
	query := `
		SELECT m.id, m.text, m.tags, m.source, m.scope, m.created_at, COALESCE(m.accessed_at, 0), m.access_count, v.distance
		FROM memory_vec v
		JOIN memory m ON m.rowid = v.rowid
		WHERE v.embedding MATCH ? AND v.superseded_by = '' AND m.superseded_by IS NULL`
	args := []any{EncodeEmbedding(qEmb)}
	if scope != ScopeAll {
		query += ` AND v.scope = ?`
		args = append(args, scope)
	}
	query += ` AND k = ? ORDER BY v.distance`
	args = append(args, limit)

	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []VectorResult
	for rows.Next() {
		var e Entry
		var dist float64
		if err := rows.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.Scope, &e.CreatedAt, &e.AccessedAt, &e.AccessCount, &dist); err != nil {
			return nil, err
		}
		// vec0 returns cosine distance (1 - similarity); invert to match the
		// brute-force path's Score, where higher is better.
		results = append(results, VectorResult{Entry: e, Score: 1 - float32(dist)})
	}
	return results, rows.Err()
}

// mirrorMessageVec writes (or refreshes) the messages_vec row for a message id
// so the index mirrors the messages.embedding BLOB. The table is created
// lazily at the embedding's width.
func (d *DB) mirrorMessageVec(id string, emb Embedding) error {
	if _, err := d.ensureVectorTables(len(emb)); err != nil {
		return err
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var rowid int64
	err = tx.QueryRow(`SELECT rowid FROM messages WHERE id = ?`, id).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // deleted concurrently; nothing to mirror
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM messages_vec WHERE rowid = ?`, rowid); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO messages_vec(rowid, embedding) VALUES (?, ?)`, rowid, EncodeEmbedding(emb)); err != nil {
		return err
	}
	return tx.Commit()
}

// searchMessagesVecIndex runs the vec0 nearest-neighbour search over messages
// and joins back to the messages table for id, session, role, content, and
// timestamp.
func (d *DB) searchMessagesVecIndex(ctx context.Context, qEmb Embedding, limit int) ([]MessageVectorResult, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT m.id, m.session_id, m.role, m.content, m.ts, v.distance
		FROM messages_vec v
		JOIN messages m ON m.rowid = v.rowid
		WHERE v.embedding MATCH ? AND k = ?
		ORDER BY v.distance`,
		EncodeEmbedding(qEmb), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []MessageVectorResult
	for rows.Next() {
		var r MessageVectorResult
		var dist float64
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Role, &r.Content, &r.Timestamp, &dist); err != nil {
			return nil, err
		}
		// vec0 returns cosine distance (1 - similarity); invert to match the
		// brute-force path's Score, where higher is better.
		r.Score = 1 - float32(dist)
		results = append(results, r)
	}
	return results, rows.Err()
}
