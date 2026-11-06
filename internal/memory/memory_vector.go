package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// VectorResult is a memory entry with its cosine similarity score.
type VectorResult struct {
	Entry
	Score float32
}

// embedError reports that the vector path could not embed its query. It is
// unexported so only this package can construct one: callers classify with
// IsEmbedFailure rather than type-asserting, and a zero value cannot be
// mistaken for a real outage.
type embedError struct{ err error }

func (e *embedError) Error() string {
	if e == nil || e.err == nil {
		return "memory: embedding failed"
	}
	return e.err.Error()
}

func (e *embedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// IsEmbedFailure reports whether err came from the query-embedding step of a
// vector search (as opposed to the store/scan step). Callers that need the
// reason for a degraded semantic path use this: SearchMemoryVectorScopedDiag
// returns it, while SearchMemoryVectorScoped deliberately degrades it to
// (nil, nil).
func IsEmbedFailure(err error) bool {
	var e *embedError
	return errors.As(err, &e) && e.err != nil
}

// ReembedStaleEmbeddings rewrites memory rows whose stored vector has a
// different dimensionality than the configured embedder produces (yaah-shq).
// Swapping the model (say 384-dim all-MiniLM for 768-dim nomic) leaves every
// earlier row uncomparable to the new query vectors — it survives on keyword
// fallback until this runs. Returns the number of rows rewritten; a nil
// embedder is a no-op. An embed failure is surfaced like any other and does
// not stop the sweep.
func (d *DB) ReembedStaleEmbeddings(ctx context.Context) (int, error) {
	embedder := d.Embedder()
	if embedder == nil {
		return 0, nil
	}
	// The probe call is the only way to learn the current model's width,
	// and it is one round-trip on a path that is about to make many.
	probe, err := embedder.Embed(ctx, "dimension probe")
	if err != nil {
		d.recordEmbedFailure("reconcile_embeddings", err)
		// Make the failure visible to the caller instead of returning a
		// clean (0, nil): stale rows would otherwise stay stale with no
		// indication the heal never ran (m3pk/shq review).
		return 0, fmt.Errorf("reembed stale: probe embedder: %w", err)
	}
	rows, err := d.sql.Query(
		`SELECT id, text FROM memory WHERE embedding IS NOT NULL AND length(embedding) != ?`, 4*len(probe))
	if err != nil {
		return 0, fmt.Errorf("reembed stale: query: %w", err)
	}
	defer rows.Close()

	type staleRow struct{ id, text string }
	var stale []staleRow
	for rows.Next() {
		var r staleRow
		if err := rows.Scan(&r.id, &r.text); err != nil {
			return 0, err
		}
		stale = append(stale, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	reembedded := 0
	for _, r := range stale {
		emb, err := embedder.Embed(ctx, r.text)
		if err != nil {
			d.recordEmbedFailure("reconcile_embeddings", err)
			continue
		}
		if _, err := d.sql.Exec(`UPDATE memory SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), r.id); err != nil {
			slog.Warn("memory: store embedding failed", "op", "reembed_stale", "id", r.id, "err", err)
			continue
		}
		if err := d.mirrorVec(r.id, emb); err != nil {
			slog.Warn("memory: mirror vec0 failed", "op", "reembed_stale", "id", r.id, "err", err)
		}
		reembedded++
	}
	return reembedded, nil
}

// SearchMemoryVector embeds the query, scans all rows with non-null
// embedding, and returns the top-K sorted by cosine similarity
// (descending). Rows whose stored vector has a different dimensionality
// than the embedder's output are scored by keyword fallback and ranked
// after the vector hits (yaah-shq). Falls back to an empty result when no embedder is
// configured or the embedding call fails; a failed embedding call is
// surfaced via the rate-limited warning log and the OTel embed-failure
// counter.
func (d *DB) SearchMemoryVector(ctx context.Context, query string, limit int) ([]VectorResult, error) {
	return d.SearchMemoryVectorScoped(ctx, query, limit, ScopeAll)
}

// SearchMemoryVectorScoped is SearchMemoryVector with an explicit scope (tier)
// filter. scope must name a tier: ScopeGlobal, ScopeSession, or ScopeAll.
//
// An embed failure degrades to (nil, nil): semantic recall is best-effort and
// the caller keeps its keyword path. Use SearchMemoryVectorScopedDiag when the
// reason for the degradation must be reported.
func (d *DB) SearchMemoryVectorScoped(ctx context.Context, query string, limit int, scope string) ([]VectorResult, error) {
	results, err := d.SearchMemoryVectorScopedDiag(ctx, query, limit, scope)
	if IsEmbedFailure(err) {
		return nil, nil
	}
	return results, err
}

// SearchMemoryVectorScopedDiag is SearchMemoryVectorScoped with the reason for
// a degraded vector search preserved. A non-nil error means the vector path
// produced nothing: either the query could not be embedded (an embed failure,
// already recorded for the log and OTel counter; classify it with
// IsEmbedFailure) or the store query failed.
// A nil embedder is not an error — it reports (nil, nil) — so a caller that
// must name that state checks Embedder() first.
func (d *DB) SearchMemoryVectorScopedDiag(ctx context.Context, query string, limit int, scope string) ([]VectorResult, error) {
	scope, err := NormalizeScopeFilter(scope)
	if err != nil {
		return nil, err
	}
	embedder := d.Embedder()
	if embedder == nil {
		return nil, nil
	}
	qEmb, err := EmbedQuery(ctx, embedder, query)
	if err != nil {
		d.recordEmbedFailure("search_memory_vector", err)
		return nil, &embedError{err: err}
	}

	// Indexed path when the vec0 table exists, brute-force scan otherwise
	// (the index is created lazily, so an embedder that only became
	// reachable after startup still serves the scan path).
	var results []VectorResult
	if d.vecIndexPresent() {
		results, err = d.searchVecIndex(ctx, qEmb, limit, scope)
	} else {
		results, err = d.searchVectorScan(ctx, qEmb, query, limit, scope)
	}
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	ids := make([]string, len(results))
	for i := range results {
		results[i].AccessCount++
		results[i].AccessedAt = now
		ids[i] = results[i].ID
	}
	d.bumpAccessCountIDs(ids, now)

	return results, nil
}

// searchVectorScan is the pre-vec0 brute-force path: it scans every row with
// a non-null embedding and scores by cosine similarity in Go, degrading
// stale-width rows to keyword overlap (yaah-shq). Kept as the fallback when
// the vec0 index has not been created yet.
func (d *DB) searchVectorScan(ctx context.Context, qEmb Embedding, query string, limit int, scope string) ([]VectorResult, error) {
	vecSQL := `
		SELECT id, text, tags, source, scope, created_at, COALESCE(accessed_at, 0), access_count, COALESCE(embedding, '')
		FROM memory
		WHERE embedding IS NOT NULL AND superseded_by IS NULL`
	var vecArgs []any
	if scope != ScopeAll {
		vecSQL += ` AND scope = ?`
		vecArgs = append(vecArgs, scope)
	}
	rows, err := d.sql.QueryContext(ctx, vecSQL, vecArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		e    Entry
		blob []byte
	}
	var candidates []candidate
	for rows.Next() {
		var e Entry
		var blob []byte
		if err := rows.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.Scope, &e.CreatedAt, &e.AccessedAt, &e.AccessCount, &blob); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate{e, blob})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Rows embedded by a previous embedding model decode to a different
	// dimensionality and cannot be compared with this query's vector.
	// CosineSimilarity returns 0 on a length mismatch, so such rows used to
	// rank last and fall off the end of the limit with no signal at all
	// (yaah-shq). Score them by keyword overlap instead and append them
	// after the vector hits: a model swap then degrades those rows to
	// keyword recall rather than losing them, and the mismatch is warned
	// about so it can be repaired.
	tokens := keywordTokens(query)
	var results, keywordResults []VectorResult
	// Collect every stale width: a store can hold rows from several past
	// models (384 and 512 and 768), and reporting only the first masked
	// the others (m3pk/shq review).
	staleByDim := map[int]int{}
	for _, c := range candidates {
		if len(c.blob) == 0 {
			// An unembedded row is as findable as this search can make it:
			// score it on words, like a stale-width row, instead of letting
			// the vector path drop it silently (shq review).
			if score, ok := keywordScore(c.e.Text, tokens); ok {
				keywordResults = append(keywordResults, VectorResult{Entry: c.e, Score: score})
			}
			continue
		}
		emb := DecodeEmbedding(c.blob)
		if len(emb) != len(qEmb) {
			staleByDim[len(emb)]++
			if score, ok := keywordScore(c.e.Text, tokens); ok {
				keywordResults = append(keywordResults, VectorResult{Entry: c.e, Score: score})
			}
			continue
		}
		score := CosineSimilarity(qEmb, emb)
		results = append(results, VectorResult{Entry: c.e, Score: score})
	}
	if len(staleByDim) > 0 {
		dims := make([]int, 0, len(staleByDim))
		for dim := range staleByDim {
			dims = append(dims, dim)
		}
		sort.Ints(dims)
		for _, dim := range dims {
			d.warnEmbedDimMismatch(dim, len(qEmb), staleByDim[dim])
		}
	}

	// Sort descending by score (sort.Slice — the hand-rolled bubble
	// sort was O(n²) on every vector search, review A6).
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	sort.Slice(keywordResults, func(i, j int) bool { return keywordResults[i].Score > keywordResults[j].Score })
	// A cosine score and a keyword-overlap score are not on the same
	// scale, so the two lists are concatenated rather than merged: every
	// vector hit outranks every keyword-fallback hit. But the limit must
	// not silently starve the fallback rows (shq review) — when stale rows
	// exist they reserve up to a third of the limit, so a page full of
	// comparable hits cannot hide them. For limit < 3 vector-first ordering
	// is preserved; the dim-mismatch warning still names the hidden rows.
	if limit > 0 && len(results)+len(keywordResults) > limit {
		reserve := limit / 3
		if reserve > len(keywordResults) {
			reserve = len(keywordResults)
		}
		vecKeep := limit - reserve
		if vecKeep > len(results) {
			vecKeep = len(results)
		}
		out := make([]VectorResult, 0, limit)
		out = append(out, results[:vecKeep]...)
		kwTake := limit - len(out)
		if kwTake > len(keywordResults) {
			kwTake = len(keywordResults)
		}
		out = append(out, keywordResults[:kwTake]...)
		results = out
	} else {
		results = append(results, keywordResults...)
	}

	return results, nil
}

// bumpAccessCountIDs records one access for every id in a single
// statement — the previous per-row UPDATE loop issued N round-trips
// per search (review A6).
func (d *DB) bumpAccessCountIDs(ids []string, now int64) {
	if len(ids) == 0 {
		return
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, now)
	for _, id := range ids {
		args = append(args, id)
	}
	if _, err := d.sql.Exec(
		`UPDATE memory SET access_count = access_count + 1, accessed_at = ? WHERE id IN (`+placeholders+`)`,
		args...); err != nil {
		// Best-effort bookkeeping: a failure only means access stats
		// drift, so the search result is still returned — but the drift
		// must not be silent.
		slog.Warn("memory: access-count bump failed", "ids", len(ids), "err", err)
	}
}

// ReconcileEmbeddings finds memory rows with NULL embedding, embeds their
// text using the configured embedder, and stores the results. It returns
// the number of rows updated. An error from any single embed does not stop
// the reconciliation; each failure is surfaced via the rate-limited
// warning log and the OTel embed-failure counter.
func (d *DB) ReconcileEmbeddings(ctx context.Context) (int, error) {
	embedder := d.Embedder()
	if embedder == nil {
		return 0, nil
	}
	rows, err := d.sql.Query(`SELECT id, text FROM memory WHERE embedding IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("reconcile: query: %w", err)
	}
	defer rows.Close()

	type row struct {
		id, text string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.text); err != nil {
			return 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	updated := 0
	for _, r := range pending {
		emb, err := embedder.Embed(ctx, r.text)
		if err != nil {
			d.recordEmbedFailure("reconcile_embeddings", err)
			continue
		}
		if _, err := d.sql.Exec(`UPDATE memory SET embedding = ? WHERE id = ?`, EncodeEmbedding(emb), r.id); err != nil {
			slog.Warn("memory: store embedding failed", "op", "reconcile_embeddings", "id", r.id, "err", err)
			continue
		}
		if err := d.mirrorVec(r.id, emb); err != nil {
			slog.Warn("memory: mirror vec0 failed", "op", "reconcile_embeddings", "id", r.id, "err", err)
		}
		updated++
	}

	// A model swap leaves rows at the old dimensionality, which the vector
	// search cannot compare; fold the repair into this sweep so the stale
	// population drains instead of persisting behind keyword fallback
	// (yaah-shq).
	healed, err := d.ReembedStaleEmbeddings(ctx)
	if err != nil {
		slog.Warn("memory: re-embed stale rows failed", "err", err)
	}
	return updated + healed, nil
}
