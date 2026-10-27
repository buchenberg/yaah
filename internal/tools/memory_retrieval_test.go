package tools

// memory_retrieval_test.go pins the retrieval-path labels memory_search
// reports (yaah-17s): a semantic hit, a missing embedder, a failed embed, a
// tag-filtered keyword search, and a vector search with no candidates must all
// be distinguishable from the tool output alone.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
)

// fixedEmbedder returns one vector for every input, so every embedded entry
// scores 1.0 against every query. That makes the semantic path hit
// deterministically without depending on model behavior.
type fixedEmbedder struct{}

func (fixedEmbedder) Embed(context.Context, string) (memory.Embedding, error) {
	return memory.Embedding{1, 0, 0}, nil
}

// brokenEmbedder stands in for an embedding server that is reachable but
// failing: the vector path must report the error, not present FTS as semantic.
type brokenEmbedder struct{ err error }

func (b brokenEmbedder) Embed(context.Context, string) (memory.Embedding, error) {
	return nil, b.err
}

func seedRetrievalEntry(t *testing.T, db *memory.DB, e memory.Entry) {
	t.Helper()
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().Unix()
	}
	if err := db.AddMemory(e); err != nil {
		t.Fatalf("AddMemory(%s): %v", e.ID, err)
	}
}

// seedEmbeddedRetrievalEntry additionally waits for the background embedding,
// so the vector path has a candidate to score.
func seedEmbeddedRetrievalEntry(t *testing.T, db *memory.DB, e memory.Entry) {
	t.Helper()
	seedRetrievalEntry(t, db, e)
	ch := db.EmbedMemoryAsync(e.ID, e.Text)
	if ch == nil {
		t.Fatalf("EmbedMemoryAsync(%s) returned nil with an embedder configured", e.ID)
	}
	<-ch
}

func TestMemorySearchTool_RetrievalPath(t *testing.T) {
	ctx := context.Background()

	t.Run("semantic hit", func(t *testing.T) {
		db := openScopeTestDB(t)
		db.SetEmbedder(fixedEmbedder{})
		seedEmbeddedRetrievalEntry(t, db, memory.Entry{ID: "g1", Text: "Postgres connection pooling", Scope: memory.ScopeGlobal})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"database connections"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.HasPrefix(out, RetrievalSemantic) {
			t.Fatalf("semantic hit must lead with the path label, got %q", out)
		}
		if !strings.Contains(out, "[semantic]") || !strings.Contains(out, "g1") {
			t.Fatalf("semantic hit must be marked [semantic] and carry the entry, got %q", out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "g1") && !strings.HasPrefix(line, "[semantic] ") {
				t.Fatalf("each semantic hit must begin with the [semantic] marker: %q", line)
			}
		}
		if strings.Contains(out, RetrievalKeyword) {
			t.Fatalf("semantic hit mislabelled as keyword: %q", out)
		}
	})

	t.Run("no embedder configured", func(t *testing.T) {
		db := openScopeTestDB(t)
		seedRetrievalEntry(t, db, memory.Entry{ID: "g1", Text: "Postgres connection pooling", Scope: memory.ScopeGlobal})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"connection"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.Contains(out, RetrievalKeyword+" — no embedding model configured") {
			t.Fatalf("missing-embedder path not named: %q", out)
		}
		if !strings.Contains(out, "g1") {
			t.Fatalf("keyword fallback lost the match: %q", out)
		}
		if strings.Contains(out, "[semantic]") {
			t.Fatalf("keyword fallback hit marked semantic: %q", out)
		}
	})

	t.Run("embed failure surfaces once and falls back", func(t *testing.T) {
		db := openScopeTestDB(t)
		db.SetEmbedder(brokenEmbedder{err: errors.New("connection refused")})
		seedRetrievalEntry(t, db, memory.Entry{ID: "g1", Text: "Postgres connection pooling", Scope: memory.ScopeGlobal})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"connection"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.Contains(out, RetrievalKeyword+" — semantic search failed (embedding): connection refused") {
			t.Fatalf("embed failure not surfaced in the path line: %q", out)
		}
		if strings.Count(out, "connection refused") != 1 {
			t.Fatalf("embed error must be surfaced once, got %q", out)
		}
		if !strings.Contains(out, "g1") {
			t.Fatalf("keyword fallback lost the match: %q", out)
		}
	})

	t.Run("embed failure with no keyword match keeps the path on the empty hint", func(t *testing.T) {
		db := openScopeTestDB(t)
		db.SetEmbedder(brokenEmbedder{err: errors.New("timeout")})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"quantumchromodynamics"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.Contains(out, "semantic search failed (embedding): timeout") {
			t.Fatalf("degraded path not named on an empty result: %q", out)
		}
		if !strings.Contains(out, "No matching memories found.") {
			t.Fatalf("empty hint missing: %q", out)
		}
	})

	t.Run("tag selects keyword-only", func(t *testing.T) {
		db := openScopeTestDB(t)
		db.SetEmbedder(fixedEmbedder{})
		seedEmbeddedRetrievalEntry(t, db, memory.Entry{
			ID:    "g1",
			Text:  "Postgres pooling decision",
			Tags:  `["decision"]`,
			Scope: memory.ScopeGlobal,
		})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"pooling","tag":"decision"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.Contains(out, RetrievalKeyword+" — tag filter selects keyword-only matching") {
			t.Fatalf("tag path not named: %q", out)
		}
		if strings.Contains(out, "[semantic]") {
			t.Fatalf("tag-filtered search ran the vector path: %q", out)
		}
		if !strings.Contains(out, "g1") {
			t.Fatalf("tag-filtered search lost the match: %q", out)
		}
	})

	t.Run("vector search with no candidates falls back", func(t *testing.T) {
		db := openScopeTestDB(t)
		db.SetEmbedder(fixedEmbedder{})
		// Added without EmbedMemoryAsync: the row is searchable by FTS but has
		// no embedding, so the vector scan finds no candidates.
		seedRetrievalEntry(t, db, memory.Entry{ID: "g1", Text: "Postgres connection pooling", Scope: memory.ScopeGlobal})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"connection"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.Contains(out, RetrievalKeyword+" — semantic search returned no candidates") {
			t.Fatalf("zero-candidate vector path not named: %q", out)
		}
		if !strings.Contains(out, "g1") {
			t.Fatalf("keyword fallback lost the match: %q", out)
		}
	})

	t.Run("empty result names the keyword path", func(t *testing.T) {
		db := openScopeTestDB(t)

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"quantumchromodynamics"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.Contains(out, RetrievalKeyword+" — no embedding model configured") {
			t.Fatalf("empty result lost the path label: %q", out)
		}
		if !strings.Contains(out, "No matching memories found.") {
			t.Fatalf("empty hint missing: %q", out)
		}
	})

	t.Run("empty query names the listing path, not FTS5", func(t *testing.T) {
		db := openScopeTestDB(t)
		seedRetrievalEntry(t, db, memory.Entry{ID: "g1", Text: "Postgres connection pooling", Scope: memory.ScopeGlobal})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"top_k":5}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.HasPrefix(out, RetrievalListing) {
			t.Fatalf("listing path not named: %q", out)
		}
		if strings.Contains(out, "(FTS5)") {
			t.Fatalf("a listing runs no FTS match and must not claim FTS5: %q", out)
		}
		if !strings.Contains(out, "g1") {
			t.Fatalf("listing lost the entry: %q", out)
		}
	})

	t.Run("tag-only listing keeps the tag in the label", func(t *testing.T) {
		db := openScopeTestDB(t)
		seedRetrievalEntry(t, db, memory.Entry{
			ID:    "g1",
			Text:  "Postgres pooling decision",
			Tags:  `["decision"]`,
			Scope: memory.ScopeGlobal,
		})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"tag":"decision"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.HasPrefix(out, RetrievalListing) {
			t.Fatalf("tag-only listing path not named: %q", out)
		}
		if !strings.Contains(out, `tag "decision" narrows the listing`) {
			t.Fatalf("tag-only listing lost the tag mention: %q", out)
		}
		if !strings.Contains(out, "g1") {
			t.Fatalf("tag-only listing lost the entry: %q", out)
		}
	})
}

// TestSemanticFailureReason distinguishes a failed embed (the semantic path is
// down) from a store failure instead of reporting both as an embedding outage.
func TestSemanticFailureReason(t *testing.T) {
	db := openScopeTestDB(t)
	db.SetEmbedder(brokenEmbedder{err: errors.New("connection refused")})
	_, diagErr := db.SearchMemoryVectorScopedDiag(context.Background(), "query", 10, memory.ScopeGlobal)
	if diagErr == nil {
		t.Fatal("expected an embed error from the diag path")
	}
	embed := semanticFailureReason(diagErr)
	if !strings.Contains(embed, "(embedding): connection refused") {
		t.Fatalf("embed failure mislabelled: %q", embed)
	}
	store := semanticFailureReason(errors.New("disk I/O error"))
	if !strings.Contains(store, "(store): disk I/O error") {
		t.Fatalf("store failure mislabelled as embedding: %q", store)
	}
}
