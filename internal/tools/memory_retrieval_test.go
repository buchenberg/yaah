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

// widthEmbedder returns a vector of a fixed width, so a test can stand on
// either side of an embedding-model swap: rows embedded at one width cannot be
// compared with a query embedded at another (yaah-shq).
type widthEmbedder struct{ dims int }

func (e widthEmbedder) Embed(context.Context, string) (memory.Embedding, error) {
	v := make(memory.Embedding, e.dims)
	for i := range v {
		v[i] = 0.5 + float32(i)*0.01
	}
	return v, nil
}

// TestMemorySearchTool_DimMismatchNoteEveryPath pins PR #824 review finding 3:
// the note has to reach every path, not only the one where the vector page has
// hits. A store whose only stale-width rows share no word with the query
// returns zero vector hits, falls through to FTS, and used to drop the note —
// the silent omission the note exists to fix.
func TestMemorySearchTool_DimMismatchNoteEveryPath(t *testing.T) {
	ctx := context.Background()

	t.Run("stale-only store with no keyword overlap notes the omission", func(t *testing.T) {
		db := openScopeTestDB(t)
		db.SetEmbedder(widthEmbedder{dims: 3})
		seedEmbeddedRetrievalEntry(t, db, memory.Entry{ID: "stale", Text: "llama grooming schedule", Scope: memory.ScopeGlobal})
		// The model swap: the query embeds at a width the stored row cannot be
		// compared with, and no word of the row matches the query, so the
		// vector page is empty and control falls through to FTS.
		db.SetEmbedder(widthEmbedder{dims: 5})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"kubernetes"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.Contains(out, "No matching memories found.") {
			t.Fatalf("the unrelated stale row must not pad the page: %q", out)
		}
		if !strings.Contains(out, "different embedding dimension") || !strings.Contains(out, "3: 1 row(s)") {
			t.Fatalf("the empty-result path dropped the dimension note: %q", out)
		}
	})

	t.Run("stale-only store with fts hits notes the omission", func(t *testing.T) {
		db := openScopeTestDB(t)
		db.SetEmbedder(widthEmbedder{dims: 3})
		seedEmbeddedRetrievalEntry(t, db, memory.Entry{ID: "stale", Text: "llama grooming schedule", Scope: memory.ScopeGlobal})
		// No embedding at all, so only FTS5 can return it: the vector page is
		// empty while the keyword path has a row, which is the fall-through
		// branch, not the empty one.
		seedRetrievalEntry(t, db, memory.Entry{ID: "kw", Text: "kubernetes cluster tuning", Scope: memory.ScopeGlobal})
		db.SetEmbedder(widthEmbedder{dims: 5})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"kubernetes"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if !strings.Contains(out, "kw") {
			t.Fatalf("the keyword path lost its row: %q", out)
		}
		if !strings.Contains(out, "different embedding dimension") || !strings.Contains(out, "3: 1 row(s)") {
			t.Fatalf("the FTS fall-through path dropped the dimension note: %q", out)
		}
	})
}

// TestMemorySearchTool_PageLabelKeepsFallback pins PR #824 review finding 4: a
// page mixing stale-embedding fallback rows with FTS rows must keep the
// fallback label. The FTS block used to overwrite it with the plain hybrid
// label as soon as it contributed a row, hiding the fact that part of the store
// cannot be compared with the current model at all.
func TestMemorySearchTool_PageLabelKeepsFallback(t *testing.T) {
	ctx := context.Background()

	t.Run("fallback row plus fts row keeps the fallback label", func(t *testing.T) {
		db := openScopeTestDB(t)
		db.SetEmbedder(widthEmbedder{dims: 3})
		// Left at the previous model's width, and it shares a word with the
		// query, so it reaches the page as a keyword-fallback row.
		seedEmbeddedRetrievalEntry(t, db, memory.Entry{ID: "stale", Text: "postgres pgbouncer tuning", Scope: memory.ScopeGlobal})
		db.SetEmbedder(widthEmbedder{dims: 5})
		// Embedded at the current width: the vector path scores it.
		seedEmbeddedRetrievalEntry(t, db, memory.Entry{ID: "vec", Text: "postgres connection pooling", Scope: memory.ScopeGlobal})
		// No embedding at all: only FTS5 can return it, so keyword contributes.
		seedRetrievalEntry(t, db, memory.Entry{ID: "ftsrow", Text: "postgres replication slots", Scope: memory.ScopeGlobal})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"postgres"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		if len(strings.Split(out, "\n")) < 2 {
			t.Fatalf("page too short to judge: %q", out)
		}
		if !strings.Contains(strings.Split(out, "\n")[0], RetrievalHybridFallback) {
			t.Fatalf("mixed page lost the fallback label: %q", out)
		}
		for _, id := range []string{"stale", "vec", "ftsrow"} {
			if !strings.Contains(out, id) {
				t.Errorf("page lost %s, which the label describes: %q", id, out)
			}
		}
	})

	t.Run("no fallback row keeps the plain hybrid label", func(t *testing.T) {
		db := openScopeTestDB(t)
		db.SetEmbedder(widthEmbedder{dims: 5})
		seedEmbeddedRetrievalEntry(t, db, memory.Entry{ID: "vec", Text: "postgres connection pooling", Scope: memory.ScopeGlobal})
		seedRetrievalEntry(t, db, memory.Entry{ID: "ftsrow", Text: "postgres replication slots", Scope: memory.ScopeGlobal})

		out, err := (&MemorySearchTool{DB: db}).Execute(ctx, `{"query":"postgres"}`)
		if err != nil {
			t.Fatalf("memory_search: %v", err)
		}
		header := strings.Split(out, "\n")[0]
		if header != RetrievalHybrid {
			t.Fatalf("header = %q, want the plain hybrid label", header)
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
