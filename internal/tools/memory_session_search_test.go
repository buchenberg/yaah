package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
)

// waitForMessageEmbedded polls SearchMessagesVector until wantID appears,
// standing in for the memory package's addMessageAndWait (which the tools
// package cannot reach). The async embed goroutine is what this waits on.
func waitForMessageEmbedded(t *testing.T, db *memory.DB, wantID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rs, err := db.SearchMessagesVector(context.Background(), "anything", 100)
		if err == nil {
			for _, r := range rs {
				if r.ID == wantID {
					return
				}
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("message %s was never embedded", wantID)
}

// TestMemorySessionSearchTool_SemanticContextAndHybridUnion pins the two
// recall fixes: a semantic hit carries the originating session and role, and a
// partial semantic page unions with FTS so a keyword-only match is not dropped.
func TestMemorySessionSearchTool_SemanticContextAndHybridUnion(t *testing.T) {
	db := openScopeTestDB(t)
	db.SetEmbedder(fixedEmbedder{}) // [1,0,0] for every input -> semantic hits at 1.0

	if err := db.CreateSession(memory.Session{ID: "ses-abcdef123456", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	// A user message that embeds (the semantic hit), and a tool message that
	// does not embed (keyword-only via FTS).
	if err := db.AddMessage(memory.Message{ID: "emb-1", SessionID: "ses-abcdef123456", Idx: 0, Role: "user", Content: "postgres connection pooling", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	waitForMessageEmbedded(t, db, "emb-1")
	if err := db.AddMessage(memory.Message{ID: "kw-1", SessionID: "ses-abcdef123456", Idx: 1, Role: "tool", ToolName: "web", Content: "connection retry policy", Timestamp: 2}); err != nil {
		t.Fatal(err)
	}

	out, err := (&MemorySessionSearchTool{DB: db}).Execute(context.Background(), `{"query":"connection","top_k":10}`)
	if err != nil {
		t.Fatalf("memory_search_sessions: %v", err)
	}

	// The semantic hit carries the session prefix and role (not content alone).
	if !strings.Contains(out, "[semantic]") {
		t.Fatalf("semantic hit missing marker: %q", out)
	}
	if !strings.Contains(out, "ses-abcdef12") || !strings.Contains(out, "user:") {
		t.Fatalf("semantic hit lost session/role context: %q", out)
	}
	// The partial semantic page unions with FTS, so the keyword-only tool
	// message is surfaced too, and the merged path is labeled.
	if !strings.Contains(out, RetrievalHybrid) {
		t.Fatalf("partial semantic page must be labeled hybrid: %q", out)
	}
	if !strings.Contains(out, "connection retry policy") {
		t.Fatalf("hybrid union dropped the keyword-only message: %q", out)
	}
}

// TestMemorySessionSearchTool_KeywordLabel verifies the keyword fallback names
// its path: a missing embedder must not read as a plain full-text search (it
// should carry the "no embedding model configured" label, like memory_search).
func TestMemorySessionSearchTool_KeywordLabel(t *testing.T) {
	db := openScopeTestDB(t) // no embedder configured

	if err := db.CreateSession(memory.Session{ID: "ses-abcdef123456", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMessage(memory.Message{ID: "kw-1", SessionID: "ses-abcdef123456", Idx: 0, Role: "user", Content: "postgres connection pooling", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}

	out, err := (&MemorySessionSearchTool{DB: db}).Execute(context.Background(), `{"query":"connection"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, RetrievalKeyword+" — no embedding model configured") {
		t.Fatalf("keyword fallback must name the missing-embedder path: %q", out)
	}
	if !strings.Contains(out, "postgres connection pooling") {
		t.Fatalf("keyword fallback lost the match: %q", out)
	}
}
