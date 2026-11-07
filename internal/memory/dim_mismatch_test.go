package memory

import (
	"context"
	"strings"
	"testing"
)

// dimEmbedder returns a fixed-width vector so a test can stand on either side
// of an embedding-model swap (yaah-shq): 384 dims for the old model, 768 for
// the new one.
type dimEmbedder struct {
	dims int
	base float32
}

func (e dimEmbedder) Embed(_ context.Context, text string) (Embedding, error) {
	v := make(Embedding, e.dims)
	for i := range v {
		v[i] = e.base + float32(i)*0.01 + float32(len(text)%7)*0.001
	}
	return v, nil
}

// storeEmbedding writes a raw embedding blob for id, standing in for a row
// embedded by a previous model (AddMemory + EmbedMemoryAsync would embed at
// the current width).
func storeEmbedding(t *testing.T, db *DB, id string, dims int) {
	t.Helper()
	vec := make(Embedding, dims)
	for i := range vec {
		vec[i] = 0.1 + float32(i)*0.01
	}
	if _, err := db.sql.Exec(`UPDATE memory SET embedding = ? WHERE id = ?`, EncodeEmbedding(vec), id); err != nil {
		t.Fatalf("store %d-dim embedding for %s: %v", dims, id, err)
	}
}

func resultIDs(results []VectorResult) []string {
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids
}

// TestSearchMemoryVector_MatchingDimsUnchanged pins the happy path: when every
// stored vector matches the query vector's width, nothing warns and every row
// is scored (no keyword fallback is involved).
func TestSearchMemoryVector_MatchingDimsUnchanged(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)
	db.SetEmbedder(dimEmbedder{dims: 384, base: 0.5})

	addMemoryAndWait(t, db, Entry{ID: "same1", Text: "postgres connection pooling", Source: "agent", CreatedAt: 1})
	addMemoryAndWait(t, db, Entry{ID: "same2", Text: "llama grooming tips", Source: "agent", CreatedAt: 2})

	results, err := db.SearchMemoryVector(context.Background(), "database", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (ids %v)", len(results), resultIDs(results))
	}
	if out := logs.String(); out != "" {
		t.Errorf("matching dims must not warn: %q", out)
	}
}

// TestSearchMemoryVector_StaleRowSurfacesViaKeywordFallback pins the yaah-shq
// fix on the indexed path: a row embedded at the previous model's width is not
// in the memory_vec index (it was recreated at the new width), so the vec0
// search cannot return it. It must still reach the page through the keyword
// fallback, and the report must count the mismatch so the omission is not
// silent.
func TestSearchMemoryVector_StaleRowSurfacesViaKeywordFallback(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)

	// A row embedded by the previous, 384-dim model.
	if err := db.AddMemory(Entry{ID: "old1", Text: "workspace sandbox denies egress by default", Source: "agent", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	storeEmbedding(t, db, "old1", 384)

	// The model swap: rows added now are embedded at 768 dims and mirrored
	// into the vec0 index at that width.
	db.SetEmbedder(dimEmbedder{dims: 768, base: 0.5})
	addMemoryAndWait(t, db, Entry{ID: "new1", Text: "sandbox flag for the sandbox tool", Source: "agent", CreatedAt: 2})

	report, err := db.SearchMemoryVectorReport(context.Background(), "sandbox", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Hits) != 2 {
		t.Fatalf("got %d hits, want 2 (ids %v)", len(report.Hits), resultIDs(report.Hits))
	}
	// Vector hits lead; the stale row follows, marked as keyword-scored.
	if report.Hits[0].ID != "new1" || report.Hits[0].Keyword {
		t.Errorf("first hit = %s (keyword=%v), want the vector-scored new1", report.Hits[0].ID, report.Hits[0].Keyword)
	}
	if report.Hits[1].ID != "old1" || !report.Hits[1].Keyword {
		t.Errorf("second hit = %s (keyword=%v), want the keyword-scored old1", report.Hits[1].ID, report.Hits[1].Keyword)
	}
	if report.DimMismatch != 1 || report.DimMismatchByDim[384] != 1 {
		t.Errorf("DimMismatch = %d (by dim %v), want 1 row at 384", report.DimMismatch, report.DimMismatchByDim)
	}
	if !strings.Contains(logs.String(), "dimensions differ") {
		t.Errorf("stale row must be warned about: %q", logs.String())
	}

	// The re-embed sweep heals the row, after which it is vector-scored again
	// and stops being counted.
	if n, err := db.ReembedStaleEmbeddings(context.Background()); err != nil || n != 1 {
		t.Fatalf("ReembedStaleEmbeddings = %d, %v; want 1 healed row", n, err)
	}
	report, err = db.SearchMemoryVectorReport(context.Background(), "sandbox", 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range report.Hits {
		seen[r.ID] = true
		if r.Keyword {
			t.Errorf("healed hit %s still keyword-scored", r.ID)
		}
	}
	if !seen["old1"] || !seen["new1"] {
		t.Fatalf("after re-embed both rows must be findable: %v", resultIDs(report.Hits))
	}
	if report.HasDimMismatch() {
		t.Errorf("healed store still reports %d mismatched rows", report.DimMismatch)
	}
}

// TestSearchMemoryVector_DimMismatchNoKeywordOverlap checks the other half of
// the fallback: an uncomparable row that shares no word with the query stays
// out of the results, but the mismatch is still warned about rather than
// silently swallowing the row.
func TestSearchMemoryVector_DimMismatchNoKeywordOverlap(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)

	if err := db.AddMemory(Entry{ID: "old2", Text: "llama grooming", Source: "agent", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	storeEmbedding(t, db, "old2", 384)

	db.SetEmbedder(dimEmbedder{dims: 768, base: 0.5})
	results, err := db.SearchMemoryVector(context.Background(), "postgres", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("unrelated stale row must not pad results: ids %v", resultIDs(results))
	}
	if !strings.Contains(logs.String(), "dimensions differ") {
		t.Errorf("stale row must still be warned about: %q", logs.String())
	}
}

// TestSearchMemoryVector_EmptyStoreNoWarn covers the empty store: no rows, no
// results, no warning.
func TestSearchMemoryVector_EmptyStoreNoWarn(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)
	db.SetEmbedder(dimEmbedder{dims: 768, base: 0.5})

	results, err := db.SearchMemoryVector(context.Background(), "anything", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("empty store returned %d results", len(results))
	}
	if out := logs.String(); out != "" {
		t.Errorf("empty store must not warn: %q", out)
	}
}

// TestSearchMemoryVector_EveryStaleWidthWarns pins PR #824 review finding 5:
// the doc promises one warning per stale width, but a single shared rate
// limiter let the first width's warning suppress every other width for the
// whole interval. The limiter is per width now, so a store left at two widths
// names both in the same interval while repeats of one width stay rate-limited.
func TestSearchMemoryVector_EveryStaleWidthWarns(t *testing.T) {
	// twoStaleWidths builds the shape: one row left at 384 dims and one at 512
	// by two past models, then a swap to 768 for the query.
	twoStaleWidths := func(t *testing.T) *DB {
		t.Helper()
		db := openTestDB(t)
		if err := db.AddMemory(Entry{ID: "old384", Text: "llama grooming", Source: "agent", CreatedAt: 1}); err != nil {
			t.Fatalf("add old384: %v", err)
		}
		storeEmbedding(t, db, "old384", 384)
		if err := db.AddMemory(Entry{ID: "old512", Text: "llama grooming roster", Source: "agent", CreatedAt: 2}); err != nil {
			t.Fatalf("add old512: %v", err)
		}
		storeEmbedding(t, db, "old512", 512)
		db.SetEmbedder(dimEmbedder{dims: 768, base: 0.3})
		return db
	}

	t.Run("two stale widths each warn in the same interval", func(t *testing.T) {
		logs := captureWarnLogs(t)
		db := twoStaleWidths(t)

		report, err := db.SearchMemoryVectorReport(context.Background(), "llama", 10)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if report.DimMismatchByDim[384] != 1 || report.DimMismatchByDim[512] != 1 {
			t.Fatalf("DimMismatchByDim = %v, want one row at each of 384 and 512", report.DimMismatchByDim)
		}
		out := logs.String()
		for _, width := range []string{"stored_dim=384", "stored_dim=512"} {
			if got := strings.Count(out, width); got != 1 {
				t.Errorf("%s warned %d time(s), want exactly 1: %q", width, got, out)
			}
		}
	})

	t.Run("repeating the same widths stays rate-limited", func(t *testing.T) {
		logs := captureWarnLogs(t)
		db := twoStaleWidths(t)

		for i := 0; i < 3; i++ {
			if _, err := db.SearchMemoryVectorReport(context.Background(), "llama", 10); err != nil {
				t.Fatalf("search %d: %v", i, err)
			}
		}
		out := logs.String()
		for _, width := range []string{"stored_dim=384", "stored_dim=512"} {
			if got := strings.Count(out, width); got != 1 {
				t.Errorf("%s warned %d time(s) across three searches, want 1: %q", width, got, out)
			}
		}
	})
}

// TestReembedStaleEmbeddings heals rows left at the old width: one pass
// rewrites them, a second pass finds nothing, and the healed row is scored by
// the vector path again (no warning).
func TestReembedStaleEmbeddings(t *testing.T) {
	db := openTestDB(t)

	if err := db.AddMemory(Entry{ID: "s1", Text: "old vector row", Source: "agent", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	storeEmbedding(t, db, "s1", 384)

	db.SetEmbedder(dimEmbedder{dims: 768, base: 0.5})
	n, err := db.ReembedStaleEmbeddings(context.Background())
	if err != nil {
		t.Fatalf("ReembedStaleEmbeddings: %v", err)
	}
	if n != 1 {
		t.Fatalf("ReembedStaleEmbeddings = %d, want 1", n)
	}

	n, err = db.ReembedStaleEmbeddings(context.Background())
	if err != nil {
		t.Fatalf("second ReembedStaleEmbeddings: %v", err)
	}
	if n != 0 {
		t.Errorf("second pass rewrote %d rows, want 0 (heal is not idempotent-broken but should be a no-op)", n)
	}

	// The healed row is comparable now: the search scores it from the
	// vector path, so nothing falls back and nothing warns.
	logs := captureWarnLogs(t)
	results, err := db.SearchMemoryVector(context.Background(), "vector", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "s1" {
		t.Fatalf("healed row not found by vector search: %v", resultIDs(results))
	}
	if out := logs.String(); out != "" {
		t.Errorf("healed rows must not warn: %q", out)
	}
}

// TestReconcileEmbeddings_HealsStaleDims proves the startup backfill repairs
// stale widths as well as NULL embeddings, so a model swap drains itself.
func TestReconcileEmbeddings_HealsStaleDims(t *testing.T) {
	db := openTestDB(t)
	if err := db.AddMemory(Entry{ID: "rs1", Text: "stale row", Source: "agent", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	storeEmbedding(t, db, "rs1", 384)

	db.SetEmbedder(dimEmbedder{dims: 768, base: 0.5})
	updated, err := db.ReconcileEmbeddings(context.Background())
	if err != nil {
		t.Fatalf("ReconcileEmbeddings: %v", err)
	}
	if updated != 1 {
		t.Fatalf("ReconcileEmbeddings = %d, want 1 (the stale row)", updated)
	}
}

// seedMixedWidthStore builds the yaah-shq store shape: two rows embedded by a
// previous 384-dim model, then a swap to a 1536-dim model whose vec0 index is
// recreated at the new width (so it holds neither stale row), then one row
// embedded at the current width.
func seedMixedWidthStore(t *testing.T, db *DB) {
	t.Helper()
	db.SetEmbedder(dimEmbedder{dims: 384, base: 0.5})
	addMemoryAndWait(t, db, Entry{ID: "old-a", Text: "pgbouncer pools postgres connections", Source: "agent", CreatedAt: 1})
	addMemoryAndWait(t, db, Entry{ID: "old-b", Text: "llama grooming schedule", Source: "agent", CreatedAt: 2})

	db.SetEmbedder(dimEmbedder{dims: 1536, base: 0.3})
	if _, err := db.EnsureVectorIndex(context.Background()); err != nil {
		t.Fatalf("EnsureVectorIndex after the model swap: %v", err)
	}
	addMemoryAndWait(t, db, Entry{ID: "new-a", Text: "postgres connection limits in the sandbox", Source: "agent", CreatedAt: 3})
}

// TestSearchMemoryVectorReport_MixedDimensions is the yaah-shq regression: a
// store holding rows from two embedding models must surface a literal-text
// match on a stale-dimension row through the keyword fallback, and must report
// how many rows it could not compare. Nothing is re-embedded on this path.
func TestSearchMemoryVectorReport_MixedDimensions(t *testing.T) {
	t.Run("literal match on a stale row surfaces via keyword fallback", func(t *testing.T) {
		logs := captureWarnLogs(t)
		db := openTestDB(t)
		seedMixedWidthStore(t, db)

		report, err := db.SearchMemoryVectorReport(context.Background(), "pgbouncer", 10)
		if err != nil {
			t.Fatal(err)
		}

		seen := map[string]bool{}
		for _, r := range report.Hits {
			seen[r.ID] = true
			// The 1536-dim row is the only one the vector path can score;
			// every other hit here must be an explicit keyword fallback.
			if r.Keyword != (r.ID != "new-a") {
				t.Errorf("hit %s has Keyword=%v; only new-a is vector-scored", r.ID, r.Keyword)
			}
		}
		if !seen["old-a"] {
			t.Fatalf("literal match on a 384-dim row was lost: ids %v", resultIDs(report.Hits))
		}
		if !seen["new-a"] {
			t.Fatalf("vector-scored row missing from the page: ids %v", resultIDs(report.Hits))
		}

		if report.DimMismatch != 2 {
			t.Errorf("DimMismatch = %d, want 2 (old-a, old-b)", report.DimMismatch)
		}
		if report.DimMismatchByDim[384] != 2 {
			t.Errorf("DimMismatchByDim = %v, want 2 rows at 384", report.DimMismatchByDim)
		}
		if !report.HasDimMismatch() {
			t.Error("HasDimMismatch must report the mismatched rows")
		}
		if !strings.Contains(logs.String(), "dimensions differ") {
			t.Errorf("mismatch must be warned about: %q", logs.String())
		}

		// No re-embedding happened: the stale rows keep their stored width.
		var stale int
		if err := db.sql.QueryRow(`SELECT COUNT(*) FROM memory WHERE length(embedding) = ?`, 4*384).Scan(&stale); err != nil {
			t.Fatal(err)
		}
		if stale != 2 {
			t.Errorf("%d rows left at 384 dims, want 2 (the search must not re-embed)", stale)
		}
	})

	t.Run("mismatch counted even when no word matches", func(t *testing.T) {
		db := openTestDB(t)
		seedMixedWidthStore(t, db)

		report, err := db.SearchMemoryVectorReport(context.Background(), "kubernetes", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range report.Hits {
			if r.Keyword {
				t.Errorf("unrelated stale row %s padded the page", r.ID)
			}
		}
		if report.DimMismatch != 2 {
			t.Errorf("DimMismatch = %d, want 2 even without a keyword match", report.DimMismatch)
		}
	})

	t.Run("matching widths report no mismatch", func(t *testing.T) {
		logs := captureWarnLogs(t)
		db := openTestDB(t)
		db.SetEmbedder(dimEmbedder{dims: 384, base: 0.5})
		addMemoryAndWait(t, db, Entry{ID: "only", Text: "pgbouncer pools postgres connections", Source: "agent", CreatedAt: 1})

		report, err := db.SearchMemoryVectorReport(context.Background(), "pgbouncer", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Hits) != 1 || report.Hits[0].Keyword {
			t.Fatalf("hits = %v, want the single vector-scored row", resultIDs(report.Hits))
		}
		if report.HasDimMismatch() || len(report.DimMismatchByDim) != 0 {
			t.Errorf("matching widths reported a mismatch: %d %v", report.DimMismatch, report.DimMismatchByDim)
		}
		if out := logs.String(); out != "" {
			t.Errorf("matching widths must not warn: %q", out)
		}
	})

	t.Run("stale rows outside the requested tier are not counted", func(t *testing.T) {
		db := openTestDB(t)
		db.SetEmbedder(dimEmbedder{dims: 384, base: 0.5})
		addMemoryAndWait(t, db, Entry{ID: "old-session", Text: "pgbouncer session note", Scope: ScopeSession, Source: "agent", CreatedAt: 1})

		db.SetEmbedder(dimEmbedder{dims: 1536, base: 0.3})
		if _, err := db.EnsureVectorIndex(context.Background()); err != nil {
			t.Fatal(err)
		}
		addMemoryAndWait(t, db, Entry{ID: "new-g", Text: "postgres limits", Source: "agent", CreatedAt: 2})

		report, err := db.SearchMemoryVectorScopedReport(context.Background(), "pgbouncer", 10, ScopeGlobal)
		if err != nil {
			t.Fatal(err)
		}
		if report.HasDimMismatch() {
			t.Errorf("global search counted %d session-scoped stale rows (%v)", report.DimMismatch, report.DimMismatchByDim)
		}
		for _, r := range report.Hits {
			if r.ID == "old-session" {
				t.Fatalf("session atom leaked into a global search: %v", resultIDs(report.Hits))
			}
		}
	})
}
