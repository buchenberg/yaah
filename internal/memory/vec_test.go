package memory

import (
	"context"
	"math"
	"testing"
)

// TestVecEnsureVectorIndex pins the startup reconciliation: no embedder means
// no table; the first ensure creates and stamps the table at the embedder's
// width; a repeat is a no-op.
func TestVecEnsureVectorIndex(t *testing.T) {
	db := openTestDB(t)

	if rec, err := db.EnsureVectorIndex(context.Background()); err != nil || rec {
		t.Fatalf("nil embedder: rec=%v err=%v, want (false, nil)", rec, err)
	}
	if db.vecIndexPresent() {
		t.Fatal("no vec0 table must exist with no embedder")
	}

	db.SetEmbedder(dimEmbedder{dims: 384, base: 0.5})
	rec, err := db.EnsureVectorIndex(context.Background())
	if err != nil {
		t.Fatalf("EnsureVectorIndex: %v", err)
	}
	if !rec {
		t.Fatal("first ensure must report recreated=true (table created)")
	}
	if !db.vecIndexPresent() {
		t.Fatal("vec0 table must exist after ensure")
	}
	if dim, ok, err := db.vecDim(); err != nil || !ok || dim != 384 {
		t.Fatalf("vecDim = %d, %v, %v; want 384, true, nil", dim, ok, err)
	}

	if rec, err := db.EnsureVectorIndex(context.Background()); err != nil || rec {
		t.Fatalf("second ensure: rec=%v err=%v, want (false, nil)", rec, err)
	}
}

// TestVecSearchScoreMatchesCosine verifies the vec0 path returns, for each hit,
// Score == 1 - distance == CosineSimilarity(query, stored), and that results
// are ordered nearest-first (highest score).
func TestVecSearchScoreMatchesCosine(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(stubEmbedder{}) // 3-dim, deterministic by text length
	addMemoryAndWait(t, db, Entry{ID: "a", Text: "alpha beta", Source: "agent", CreatedAt: 1})
	addMemoryAndWait(t, db, Entry{ID: "b", Text: "gamma", Source: "agent", CreatedAt: 2})

	results, err := db.SearchMemoryVector(context.Background(), "query", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (%v)", len(results), resultIDs(results))
	}
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Fatalf("scores not descending: %+v", results)
		}
	}

	q, err := db.Embedder().Embed(context.Background(), "query")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		var blob []byte
		if err := db.sql.QueryRow(`SELECT embedding FROM memory WHERE id = ?`, r.ID).Scan(&blob); err != nil {
			t.Fatal(err)
		}
		want := CosineSimilarity(q, DecodeEmbedding(blob))
		if math.Abs(float64(r.Score-want)) > 1e-4 {
			t.Errorf("score for %s = %f, want %f (1-distance)", r.ID, r.Score, want)
		}
	}
}

// TestVecScopeFilter verifies the scope tier is a vec0 metadata filter, so a
// default (global) search excludes session atoms and an all-tier search spans
// both.
func TestVecScopeFilter(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(stubEmbedder{})
	addMemoryAndWait(t, db, Entry{ID: "g1", Text: "global fact", Scope: ScopeGlobal, Source: "agent", CreatedAt: 1})
	addMemoryAndWait(t, db, Entry{ID: "s1", Text: "session note", Scope: ScopeSession, Source: "agent", CreatedAt: 2})

	got, err := db.SearchMemoryVectorScoped(context.Background(), "fact", 10, ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "g1" {
		t.Fatalf("scope=global returned %v, want [g1]", resultIDs(got))
	}

	got, err = db.SearchMemoryVectorScoped(context.Background(), "fact", 10, ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("scope=all returned %d rows %v, want 2", len(got), resultIDs(got))
	}
}

// TestVecModelSwapRecreates verifies a width change drops and recreates the
// vec0 table at the new width and restamps schema_meta.
func TestVecModelSwapRecreates(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(dimEmbedder{dims: 384, base: 0.5})
	addMemoryAndWait(t, db, Entry{ID: "m1", Text: "swap me", Source: "agent", CreatedAt: 1})

	if dim, ok, _ := db.vecDim(); !ok || dim != 384 {
		t.Fatalf("dim = %d, want 384", dim)
	}

	db.SetEmbedder(dimEmbedder{dims: 768, base: 0.5})
	rec, err := db.EnsureVectorIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rec {
		t.Fatal("model swap must recreate the index")
	}
	if dim, ok, _ := db.vecDim(); !ok || dim != 768 {
		t.Fatalf("dim = %d, want 768 after swap", dim)
	}
}

// TestVecDeleteRemovesIndexRow verifies deleting a memory removes its vec0 row,
// so the row cannot resurface (including via a later rowid reuse).
func TestVecDeleteRemovesIndexRow(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(stubEmbedder{})
	addMemoryAndWait(t, db, Entry{ID: "d1", Text: "delete me", Source: "agent", CreatedAt: 1})

	if got, _ := db.SearchMemoryVector(context.Background(), "delete", 10); len(got) != 1 {
		t.Fatalf("precondition: got %v, want [d1]", resultIDs(got))
	}

	if err := db.DeleteMemory("d1"); err != nil {
		t.Fatal(err)
	}
	got, err := db.SearchMemoryVector(context.Background(), "delete", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("deleted memory still surfaces: %v", resultIDs(got))
	}
}

// TestVecSupersedeRemovesIndexRow verifies a superseded atom drops out of the
// vector index (its vec0 row is deleted), so it never surfaces again.
func TestVecSupersedeRemovesIndexRow(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(stubEmbedder{})
	addMemoryAndWait(t, db, Entry{ID: "old", Text: "old fact", Source: "agent", CreatedAt: 1})
	addMemoryAndWait(t, db, Entry{ID: "new", Text: "new fact", Source: "agent", CreatedAt: 2})

	if err := db.SupersedeMemory("old", "new"); err != nil {
		t.Fatal(err)
	}

	got, err := db.SearchMemoryVector(context.Background(), "fact", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.ID == "old" {
			t.Fatalf("superseded atom surfaced: %v", resultIDs(got))
		}
	}
}

// TestVecBackfillOnEnsure reproduces the upgrade path: rows embedded before the
// vec0 index existed (their BLOB is populated, the index is not) must become
// findable when EnsureVectorIndex creates the table, without a re-embed pass.
func TestVecBackfillOnEnsure(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(stubEmbedder{}) // 3-dim

	if err := db.AddMemory(Entry{ID: "up1", Text: "pre-existing fact", Source: "agent", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	storeEmbedding(t, db, "up1", 3) // written by the pre-index code path

	// Before the index exists the search uses the scan path and finds it.
	if got, _ := db.SearchMemoryVector(context.Background(), "fact", 10); len(got) != 1 {
		t.Fatalf("precondition (scan path): got %v, want [up1]", resultIDs(got))
	}

	// EnsureVectorIndex creates the table and backfills the existing BLOB.
	if _, err := db.EnsureVectorIndex(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The indexed path must now find it too (backfilled, not empty).
	got, err := db.SearchMemoryVector(context.Background(), "fact", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "up1" {
		t.Fatalf("backfilled index did not surface the pre-existing row: %v", resultIDs(got))
	}
}

// TestVecStaleIndexFallsBackToScan verifies a vec0 table left at the wrong
// width (a model swap the startup reconcile could not run) degrades to the
// brute-force scan instead of returning a hard MATCH error.
func TestVecStaleIndexFallsBackToScan(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(dimEmbedder{dims: 3, base: 0.5})
	addMemoryAndWait(t, db, Entry{ID: "x1", Text: "stale width row", Source: "agent", CreatedAt: 1})

	// Swap width without reconciling: the vec0 table stays at 3 dims while
	// the query embeds at 4, so the MATCH would error.
	db.SetEmbedder(dimEmbedder{dims: 4, base: 0.5})

	if _, err := db.SearchMemoryVector(context.Background(), "row", 10); err != nil {
		t.Fatalf("stale index must degrade to the scan, not error: %v", err)
	}
}

// TestVecMessagesSearchScore verifies the messages_vec path returns, for each
// hit, Score == 1 - distance == CosineSimilarity(query, stored).
func TestVecMessagesSearchScore(t *testing.T) {
	db := openTestDB(t) // creates the ses-test session
	db.SetEmbedder(stubEmbedder{})
	addMessageAndWait(t, db, Message{ID: "m1", SessionID: "ses-test", Idx: 0, Role: "user", Content: "postgres connection pooling", Timestamp: 1})
	addMessageAndWait(t, db, Message{ID: "m2", SessionID: "ses-test", Idx: 1, Role: "user", Content: "llama grooming", Timestamp: 2})

	results, err := db.SearchMessagesVector(context.Background(), "database", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (%v)", len(results), resultIDs(results))
	}

	q, err := db.Embedder().Embed(context.Background(), "database")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		var blob []byte
		if err := db.sql.QueryRow(`SELECT embedding FROM messages WHERE id = ?`, r.ID).Scan(&blob); err != nil {
			t.Fatal(err)
		}
		want := CosineSimilarity(q, DecodeEmbedding(blob))
		if math.Abs(float64(r.Score-want)) > 1e-4 {
			t.Errorf("score for %s = %f, want %f", r.ID, r.Score, want)
		}
	}
}

// TestVecMessagesBackfillOnEnsure verifies a message embedded before the index
// existed becomes findable when EnsureVectorIndex creates and backfills it.
func TestVecMessagesBackfillOnEnsure(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(stubEmbedder{}) // 3-dim

	if err := db.AddMessage(Message{ID: "m1", SessionID: "ses-test", Idx: 0, Role: "user", Content: "pre-existing message", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	// Written by the pre-index code path: a BLOB with no vec0 mirror.
	vec := Embedding{0.1, 0.2, -0.3}
	if _, err := db.sql.Exec(`UPDATE messages SET embedding = ? WHERE id = 'm1'`, EncodeEmbedding(vec)); err != nil {
		t.Fatal(err)
	}

	if _, err := db.EnsureVectorIndex(context.Background()); err != nil {
		t.Fatal(err)
	}

	results, err := db.SearchMessagesVector(context.Background(), "message", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "m1" {
		t.Fatalf("backfilled messages index did not surface the pre-existing message: %v", resultIDs(results))
	}
}

// TestVecMessagesDeleteRemovesIndexRow verifies deleting a message removes its
// messages_vec row, so it cannot resurface (including via a reused rowid).
func TestVecMessagesDeleteRemovesIndexRow(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(stubEmbedder{})
	addMessageAndWait(t, db, Message{ID: "m1", SessionID: "ses-test", Idx: 0, Role: "user", Content: "delete this message", Timestamp: 1})

	if got, _ := db.SearchMessagesVector(context.Background(), "delete", 10); len(got) != 1 {
		t.Fatalf("precondition: got %v, want [m1]", resultIDs(got))
	}

	if err := db.DeleteMessagesFrom("ses-test", 0); err != nil {
		t.Fatal(err)
	}

	got, err := db.SearchMessagesVector(context.Background(), "delete", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("deleted message still surfaces: %v", resultIDs(got))
	}
}
