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

// memoryVecTable is the vec0 virtual table backing semantic memory search.
const memoryVecTable = "memory_vec"

// vectorDimKey is the schema_meta key holding the embedding width the
// memory_vec table was created for. vec0 pins its dimension at CREATE, so the
// width must be reconciled against the configured embedder (see
// ensureVectorTable) — a model swap changes it.
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

// EnsureVectorIndex creates (or recreates) the memory_vec table at the width
// the configured embedder produces, and stamps that width. It is the startup
// reconciliation point: a model swap changes the width, so a stale table is
// dropped and recreated for the follow-up re-embed pass to repopulate.
//
// Idempotent and safe to call repeatedly. Returns recreated=true when the
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
	return d.ensureVectorTable(len(probe))
}

// ensureVectorTable makes memory_vec exist at exactly dim, recreating it if
// the current table was created at a different width. It serializes on vecMu
// because DDL (CREATE/DROP VIRTUAL TABLE) is a shared write and concurrent
// embed goroutines may reach it simultaneously.
func (d *DB) ensureVectorTable(dim int) (bool, error) {
	d.vecMu.Lock()
	defer d.vecMu.Unlock()

	stamped, hasStamp, err := d.vecDim()
	if err != nil {
		return false, err
	}
	exists, err := d.tableExists(memoryVecTable)
	if err != nil {
		return false, err
	}
	if exists && hasStamp && stamped == dim {
		return false, nil
	}
	if exists {
		if _, err := d.sql.Exec(`DROP TABLE IF EXISTS ` + memoryVecTable); err != nil {
			return false, fmt.Errorf("vec0 index: drop stale table: %w", err)
		}
	}
	if _, err := d.sql.Exec(fmt.Sprintf(memoryVecDDL, dim)); err != nil {
		return false, fmt.Errorf("vec0 index: create table: %w", err)
	}
	if err := d.setVecDim(dim); err != nil {
		return false, err
	}
	return true, nil
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

// mirrorVec writes (or refreshes) the vec0 row for a memory id so the index
// mirrors the memory.embedding BLOB plus its scope and supersession state.
// The table is created lazily at the embedding's width, so the write path
// self-heals when the embedder only became reachable after startup.
func (d *DB) mirrorVec(id string, emb Embedding) error {
	if _, err := d.ensureVectorTable(len(emb)); err != nil {
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

// deleteVecByRowID removes the vec0 row for a memory rowid. Best-effort: a
// missing table or row is a no-op. Required on delete so a stale rowid
// (reused by a later insert) cannot resurface an old vector.
func (d *DB) deleteVecByRowID(rowid int64) {
	if !d.vecIndexPresent() {
		return
	}
	if _, err := d.sql.Exec(`DELETE FROM memory_vec WHERE rowid = ?`, rowid); err != nil {
		slog.Warn("memory: delete vec0 row failed", "rowid", rowid, "err", err)
	}
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
		WHERE v.embedding MATCH ? AND v.superseded_by = ''`
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
