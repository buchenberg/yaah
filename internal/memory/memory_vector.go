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

// VectorResult is a memory entry with its similarity score. A hit whose stored
// embedding could not be compared with the query vector — a previous embedding
// model wrote it at a different width — is marked Keyword and carries a
// keyword-overlap score instead of a cosine one (yaah-shq).
type VectorResult struct {
	Entry
	Score float32
	// Keyword marks a keyword-fallback hit: the row's stored embedding cannot be
	// compared with the current model's query vector, so it is ranked by word
	// overlap rather than dropped from the page.
	Keyword bool
}

// VectorSearchReport is one vector search: the ranked page plus the counts that
// let a caller tell a degraded recall from a true miss (yaah-shq). A row left
// at a previous embedding model's width is incomparable to the current query
// vector, so it is counted here (and keyword-scored) instead of vanishing
// without a signal.
type VectorSearchReport struct {
	// Hits is the result page: vector-scored rows first (descending cosine
	// similarity), then the keyword-scored rows the vector path could not
	// compare.
	Hits []VectorResult
	// DimMismatch counts the in-scope, live rows whose stored embedding width
	// differs from the current model's query vector. Each one is keyword-scored;
	// none is re-embedded here.
	DimMismatch int
	// DimMismatchByDim maps a stored embedding width to its row count, so a
	// store holding rows from several past models reports every width.
	DimMismatchByDim map[int]int
}

// HasDimMismatch reports whether any in-scope row was left at a previous
// embedding model's width. A caller that shows results uses it to name the
// omission rather than presenting keyword fallback as semantic recall.
func (r VectorSearchReport) HasDimMismatch() bool { return r.DimMismatch > 0 }

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
// fallback until this runs. It also re-mirrors rows whose BLOB is the correct
// width but whose vec0 row is missing (a crash between the BLOB update and the
// mirror would otherwise leave them invisible to the index-preferred search).
// Returns the number of rows healed; a nil embedder is a no-op. An embed
// failure is surfaced like any other and does not stop the sweep.
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
	dim := len(probe)
	rows, err := d.sql.Query(
		`SELECT id, text FROM memory WHERE embedding IS NOT NULL AND length(embedding) != ?`, 4*dim)
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

	// Re-mirror rows whose BLOB is the correct width but which never landed a
	// vec0 row. A single INSERT...SELECT repairs them without re-embedding. The
	// superseded_by IS NULL predicate keeps superseded atoms out: their vec0
	// rows were removed by supersede on purpose, and re-mirroring them would
	// just re-accumulate dead rows each startup.
	remirrored := 0
	if d.vecIndexPresent() {
		res, err := d.sql.ExecContext(ctx, `
			INSERT INTO memory_vec(rowid, embedding, scope, superseded_by)
			SELECT m.rowid, m.embedding, m.scope, COALESCE(m.superseded_by, '')
			FROM memory m
			WHERE m.embedding IS NOT NULL AND length(m.embedding) = ?
			  AND m.superseded_by IS NULL
			  AND NOT EXISTS (SELECT 1 FROM memory_vec v WHERE v.rowid = m.rowid)`, 4*dim)
		if err != nil {
			return reembedded, fmt.Errorf("reembed stale: re-mirror missing: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			remirrored = int(n)
		}
	}

	return reembedded + remirrored, nil
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

// SearchMemoryVectorReport is SearchMemoryVector with the dimension-mismatch
// diagnostics attached (yaah-shq).
func (d *DB) SearchMemoryVectorReport(ctx context.Context, query string, limit int) (VectorSearchReport, error) {
	return d.SearchMemoryVectorScopedReport(ctx, query, limit, ScopeAll)
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
	report, err := d.SearchMemoryVectorScopedReport(ctx, query, limit, scope)
	if err != nil {
		return nil, err
	}
	return report.Hits, nil
}

// SearchMemoryVectorScopedReport is the search the front-ends call: the ranked
// page together with the count of rows the vector path could not compare
// (yaah-shq). An embedding-model swap leaves earlier rows at the old width:
// the vec0 index is recreated at the new width (so it holds none of them) and a
// cosine score against the new query vector is meaningless. Those rows are
// keyword-scored into the page and counted in the report — the omission is
// never silent, and nothing is re-embedded here (no embedder round-trip beyond
// the query itself).
//
// A nil embedder reports the zero report; an embed failure returns an
// *embedError after recording it, exactly like the Diag form.
func (d *DB) SearchMemoryVectorScopedReport(ctx context.Context, query string, limit int, scope string) (VectorSearchReport, error) {
	scope, err := NormalizeScopeFilter(scope)
	if err != nil {
		return VectorSearchReport{}, err
	}
	embedder := d.Embedder()
	if embedder == nil {
		return VectorSearchReport{}, nil
	}
	qEmb, err := EmbedQuery(ctx, embedder, query)
	if err != nil {
		d.recordEmbedFailure("search_memory_vector", err)
		return VectorSearchReport{}, &embedError{err: err}
	}

	// Indexed path when the vec0 table exists, brute-force scan otherwise
	// (the index is created lazily, so an embedder that only became
	// reachable after startup still serves the scan path).
	var report VectorSearchReport
	if d.vecIndexPresent() {
		report, err = d.searchVecIndexReport(ctx, qEmb, query, limit, scope)
		if err != nil {
			// A stale index (wrong width after a model swap the startup
			// reconcile could not fix, or any vec0 store error) degrades to
			// the brute-force scan rather than failing the search.
			report, err = d.searchVectorScan(ctx, qEmb, query, limit, scope)
		}
	} else {
		report, err = d.searchVectorScan(ctx, qEmb, query, limit, scope)
	}
	if err != nil {
		return VectorSearchReport{}, err
	}

	// One warning per stale width, rate-limited: the condition repeats on
	// every search until the rows are re-embedded, so it must be visible
	// without flooding the log.
	d.publishStaleWidths(report.DimMismatchByDim, len(qEmb))

	now := time.Now().Unix()
	ids := make([]string, len(report.Hits))
	for i := range report.Hits {
		report.Hits[i].AccessCount++
		report.Hits[i].AccessedAt = now
		ids[i] = report.Hits[i].ID
	}
	d.bumpAccessCountIDs(ids, now)

	return report, nil
}

// publishStaleWidths surfaces the widths a store was left at by previous
// embedding models, one warning per width, so the rows that fall back to
// keyword scoring are visible instead of silently skipped (yaah-shq).
func (d *DB) publishStaleWidths(byDim map[int]int, queryDim int) {
	if len(byDim) == 0 {
		return
	}
	dims := make([]int, 0, len(byDim))
	for dim := range byDim {
		dims = append(dims, dim)
	}
	sort.Ints(dims)
	for _, dim := range dims {
		d.warnEmbedDimMismatch(dim, queryDim, byDim[dim])
	}
}

// staleWidths is the dimension-mismatch population of one search: the
// keyword-scored rows it contributes and the count per stored width.
type staleWidths struct {
	rows  []VectorResult
	count int
	byDim map[int]int
}

// staleWidthRows selects the in-scope, live rows whose stored embedding was
// written at a different width than qEmb and scores them by keyword overlap
// with query. The vec0 path needs it because the index is recreated at the
// current model's width, so no stale row is in it: without this query those
// rows are unreachable from an indexed search (yaah-shq). skip names the ids
// the vector path already returned, so a row that is both indexed and stale is
// never listed twice. Counting is the point, so the query is not limited by the
// page size.
func (d *DB) staleWidthRows(ctx context.Context, qEmb Embedding, query string, scope string, skip []VectorResult) (staleWidths, error) {
	staleSQL := `
		SELECT id, text, tags, source, scope, created_at, COALESCE(accessed_at, 0), access_count, length(embedding)
		FROM memory
		WHERE embedding IS NOT NULL AND length(embedding) != ? AND superseded_by IS NULL`
	args := []any{4 * len(qEmb)}
	if scope != ScopeAll {
		staleSQL += ` AND scope = ?`
		args = append(args, scope)
	}
	rows, err := d.sql.QueryContext(ctx, staleSQL, args...)
	if err != nil {
		return staleWidths{}, err
	}
	defer rows.Close()

	scored := make(map[string]bool, len(skip))
	for _, r := range skip {
		scored[r.ID] = true
	}
	tokens := keywordTokens(query)
	out := staleWidths{byDim: map[int]int{}}
	for rows.Next() {
		var e Entry
		var blobLen int
		if err := rows.Scan(&e.ID, &e.Text, &e.Tags, &e.Source, &e.Scope, &e.CreatedAt, &e.AccessedAt, &e.AccessCount, &blobLen); err != nil {
			return staleWidths{}, err
		}
		out.count++
		out.byDim[blobLen/4]++
		if scored[e.ID] {
			continue
		}
		// A stale row that shares no word with the query stays out of the
		// page; the count in the report is what keeps it from being
		// invisible.
		if score, ok := keywordScore(e.Text, tokens); ok {
			out.rows = append(out.rows, VectorResult{Entry: e, Score: score, Keyword: true})
		}
	}
	if err := rows.Err(); err != nil {
		return staleWidths{}, err
	}
	sort.Slice(out.rows, func(i, j int) bool { return out.rows[i].Score > out.rows[j].Score })
	return out, nil
}

// searchVecIndexReport runs the indexed KNN path and folds in the rows the
// index cannot hold (yaah-shq).
func (d *DB) searchVecIndexReport(ctx context.Context, qEmb Embedding, query string, limit int, scope string) (VectorSearchReport, error) {
	hits, err := d.searchVecIndex(ctx, qEmb, limit, scope)
	if err != nil {
		return VectorSearchReport{}, err
	}
	stale, err := d.staleWidthRows(ctx, qEmb, query, scope, hits)
	if err != nil {
		return VectorSearchReport{}, err
	}
	return VectorSearchReport{
		Hits:             mergeKeywordFallback(limit, hits, stale.rows),
		DimMismatch:      stale.count,
		DimMismatchByDim: stale.byDim,
	}, nil
}

// mergeKeywordFallback concatenates the vector-ranked page with the
// keyword-scored fallback rows. A cosine score and a keyword-overlap score are
// not on the same scale, so the lists are concatenated rather than merged:
// every vector hit outranks every fallback hit. But the limit must not silently
// starve the fallback rows — the rows it recovers are the ones a model swap
// would otherwise lose — so they reserve up to a third of the page. For
// limit < 3 the vector-first order wins and the reported mismatch count is the
// signal instead.
func mergeKeywordFallback(limit int, vec, keyword []VectorResult) []VectorResult {
	if len(keyword) == 0 {
		return vec
	}
	if limit <= 0 || len(vec)+len(keyword) <= limit {
		out := make([]VectorResult, 0, len(vec)+len(keyword))
		out = append(out, vec...)
		return append(out, keyword...)
	}
	reserve := limit / 3
	if reserve > len(keyword) {
		reserve = len(keyword)
	}
	vecKeep := limit - reserve
	if vecKeep > len(vec) {
		vecKeep = len(vec)
	}
	out := make([]VectorResult, 0, limit)
	out = append(out, vec[:vecKeep]...)
	kwTake := limit - len(out)
	if kwTake > len(keyword) {
		kwTake = len(keyword)
	}
	return append(out, keyword[:kwTake]...)
}

// searchVectorScan is the pre-vec0 brute-force path: it scans every row with
// a non-null embedding and scores by cosine similarity in Go, degrading
// stale-width rows to keyword overlap (yaah-shq). Kept as the fallback when
// the vec0 index has not been created yet.
func (d *DB) searchVectorScan(ctx context.Context, qEmb Embedding, query string, limit int, scope string) (VectorSearchReport, error) {
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
		return VectorSearchReport{}, err
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
			return VectorSearchReport{}, err
		}
		candidates = append(candidates, candidate{e, blob})
	}
	if err := rows.Err(); err != nil {
		return VectorSearchReport{}, err
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
	mismatched := 0
	for _, c := range candidates {
		if len(c.blob) == 0 {
			// An unembedded row is as findable as this search can make it:
			// score it on words, like a stale-width row, instead of letting
			// the vector path drop it silently (shq review).
			if score, ok := keywordScore(c.e.Text, tokens); ok {
				keywordResults = append(keywordResults, VectorResult{Entry: c.e, Score: score, Keyword: true})
			}
			continue
		}
		emb := DecodeEmbedding(c.blob)
		if len(emb) != len(qEmb) {
			mismatched++
			staleByDim[len(emb)]++
			if score, ok := keywordScore(c.e.Text, tokens); ok {
				keywordResults = append(keywordResults, VectorResult{Entry: c.e, Score: score, Keyword: true})
			}
			continue
		}
		score := CosineSimilarity(qEmb, emb)
		results = append(results, VectorResult{Entry: c.e, Score: score})
	}

	// Sort descending by score (sort.Slice — the hand-rolled bubble
	// sort was O(n²) on every vector search, review A6).
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	sort.Slice(keywordResults, func(i, j int) bool { return keywordResults[i].Score > keywordResults[j].Score })

	return VectorSearchReport{
		Hits:             mergeKeywordFallback(limit, results, keywordResults),
		DimMismatch:      mismatched,
		DimMismatchByDim: staleByDim,
	}, nil
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
