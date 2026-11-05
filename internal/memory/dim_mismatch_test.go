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

// TestSearchMemoryVector_DimMismatchFallsBackToKeyword reproduces the bug:
// rows were embedded at 384 dims, the embedder is swapped for a 768-dim model.
// Those rows must stay findable (keyword fallback, ranked after vector hits)
// and the mismatch must be visible in the log.
func TestSearchMemoryVector_DimMismatchFallsBackToKeyword(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)

	// A row embedded by the previous, 384-dim model.
	if err := db.AddMemory(Entry{ID: "old1", Text: "workspace sandbox denies egress by default", Source: "agent", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	storeEmbedding(t, db, "old1", 384)

	// The model swap: rows added now are embedded at 768 dims.
	db.SetEmbedder(dimEmbedder{dims: 768, base: 0.5})
	addMemoryAndWait(t, db, Entry{ID: "new1", Text: "sandbox flag for the sandbox tool", Source: "agent", CreatedAt: 2})

	results, err := db.SearchMemoryVector(context.Background(), "sandbox", 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.ID] = true
	}
	if !seen["old1"] {
		t.Fatalf("384-dim memory vanished after the model swap: ids %v", resultIDs(results))
	}
	if !seen["new1"] {
		t.Fatalf("768-dim memory missing: ids %v", resultIDs(results))
	}
	if results[0].ID != "new1" {
		t.Errorf("vector hit must outrank the keyword fallback, order = %v", resultIDs(results))
	}

	out := logs.String()
	if !strings.Contains(out, "dimensions differ") {
		t.Errorf("no dim-mismatch warning emitted: %q", out)
	}
	if !strings.Contains(out, "rows=1") {
		t.Errorf("warning must name how many rows fell back: %q", out)
	}
	if !strings.Contains(out, "stored_dim=384") || !strings.Contains(out, "query_dim=768") {
		t.Errorf("warning must name both widths: %q", out)
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
