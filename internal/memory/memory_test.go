package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestDB_OpenCreatesDatabase(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	// File should exist
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created: %v", err)
	}
}

func TestDB_AddMemoryAndSearch(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	// Add a memory entry
	entry := Entry{
		ID:        "mem-1",
		Text:      "the user prefers dark mode",
		Tags:      `["preferences","ui"]`,
		Source:    "cli",
		CreatedAt: time.Now().Unix(),
	}
	if err := db.AddMemory(entry); err != nil {
		t.Fatalf("AddMemory() error: %v", err)
	}

	// Search for it
	results, err := db.SearchMemory("dark mode", 10)
	if err != nil {
		t.Fatalf("SearchMemory() error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Text != "the user prefers dark mode" {
		t.Errorf("result text = %q", results[0].Text)
	}
}

func TestDB_AddMemoryAndSearchByTag(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	entry := Entry{
		ID:        "mem-2",
		Text:      "project uses Go for backend",
		Tags:      `["tech","go"]`,
		Source:    "cli",
		CreatedAt: time.Now().Unix(),
	}
	db.AddMemory(entry)

	results, err := db.SearchMemory("Go", 10)
	if err != nil {
		t.Fatalf("SearchMemory() error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestDB_SearchReturnsEmptyForNoMatch(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	results, err := db.SearchMemory("nonexistent", 10)
	if err != nil {
		t.Fatalf("SearchMemory() error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestDB_ListMemory(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	for i := 0; i < 5; i++ {
		db.AddMemory(Entry{
			ID:        fmt.Sprintf("mem-%d", i),
			Text:      fmt.Sprintf("memory entry %d", i),
			CreatedAt: int64(i),
		})
	}

	entries, err := db.ListMemory(3)
	if err != nil {
		t.Fatalf("ListMemory() error: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

func TestDB_CreateSessionAndAddMessage(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	sess := Session{
		ID:        "sess-1",
		StartedAt: time.Now().Unix(),
		CWD:       "/tmp/test",
		Model:     "gpt-4o-mini",
	}
	if err := db.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	msg := Message{
		SessionID: "sess-1",
		Idx:       0,
		Role:      "user",
		Content:   "hello",
		Timestamp: time.Now().Unix(),
	}
	if err := db.AddMessage(msg); err != nil {
		t.Fatalf("AddMessage() error: %v", err)
	}

	msgs, err := db.GetMessages("sess-1")
	if err != nil {
		t.Fatalf("GetMessages() error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Errorf("message content = %q", msgs[0].Content)
	}
}

func TestDB_ListSessions(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	for i := 0; i < 3; i++ {
		db.CreateSession(Session{
			ID:        fmt.Sprintf("sess-%d", i),
			StartedAt: int64(i),
		})
	}

	sessions, err := db.ListSessions(10)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(sessions))
	}
}

func TestDB_AddMemoryDedup_SkipsDuplicate(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	e1 := Entry{
		ID:        "mem-1",
		Text:      "User's name is Greg",
		Source:    "agent",
		CreatedAt: time.Now().Unix(),
	}
	if err := db.AddMemory(e1); err != nil {
		t.Fatalf("AddMemory() error: %v", err)
	}

	e2 := Entry{
		ID:        "mem-2",
		Text:      "User's name is Greg",
		Source:    "agent",
		CreatedAt: time.Now().Unix(),
	}
	dupID, err := db.AddMemoryDedup(e2)
	if err != nil {
		t.Fatalf("AddMemoryDedup() error: %v", err)
	}
	if dupID != "mem-1" {
		t.Errorf("expected duplicate ID mem-1, got %q", dupID)
	}

	all, _ := db.ListMemory(10)
	if len(all) != 1 {
		t.Errorf("expected 1 memory after dedup, got %d", len(all))
	}
}

func TestDB_AddMemoryDedup_AddsWhenNoDuplicate(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.AddMemory(Entry{
		ID: "mem-1", Text: "User's name is Greg", Source: "agent",
		CreatedAt: time.Now().Unix(),
	})

	e := Entry{
		ID: "mem-2", Text: "Project uses Go for backend", Source: "agent",
		CreatedAt: time.Now().Unix(),
	}
	dupID, err := db.AddMemoryDedup(e)
	if err != nil {
		t.Fatalf("AddMemoryDedup() error: %v", err)
	}
	if dupID != "" {
		t.Errorf("expected no duplicate, got %q", dupID)
	}

	all, _ := db.ListMemory(10)
	if len(all) != 2 {
		t.Errorf("expected 2 memories, got %d", len(all))
	}
}

func TestDB_SearchMemory_TracksAccess(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.AddMemory(Entry{
		ID: "mem-1", Text: "the user prefers dark mode",
		Source: "cli", CreatedAt: time.Now().Unix(),
	})

	results1, _ := db.SearchMemory("dark mode", 10)
	if len(results1) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results1))
	}
	if results1[0].AccessCount != 1 {
		t.Errorf("expected AccessCount=1 after first search, got %d", results1[0].AccessCount)
	}
	if results1[0].AccessedAt == 0 {
		t.Error("expected AccessedAt to be set after search")
	}

	results2, _ := db.SearchMemory("dark mode", 10)
	if len(results2) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results2))
	}
	if results2[0].AccessCount != 2 {
		t.Errorf("expected AccessCount=2 after second search, got %d", results2[0].AccessCount)
	}
}

func TestDB_DeleteMemory(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.AddMemory(Entry{
		ID: "mem-1", Text: "test memory", Source: "cli",
		CreatedAt: time.Now().Unix(),
	})

	if err := db.DeleteMemory("mem-1"); err != nil {
		t.Fatalf("DeleteMemory() error: %v", err)
	}

	all, _ := db.ListMemory(10)
	if len(all) != 0 {
		t.Errorf("expected 0 memories after delete, got %d", len(all))
	}

	// Deleting again must error (#328): a delete matching no rows used
	// to report success, which let truncated-ID deletes fake-succeed
	// while the row — and duplicates of it — accumulated.
	if err := db.DeleteMemory("mem-1"); !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("DeleteMemory() on non-existent = %v, want memory-not-found error", err)
	}
}

func TestDB_UpdateMemory(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.AddMemory(Entry{
		ID: "mem-1", Text: "User's name is Greg", Source: "agent",
		CreatedAt: time.Now().Unix(),
	})

	if err := db.UpdateMemory("mem-1", "User's name is Greg Buchenberger"); err != nil {
		t.Fatalf("UpdateMemory() error: %v", err)
	}

	// Search for updated text
	results, _ := db.SearchMemory("Buchenberger", 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result after update, got %d", len(results))
	}
	if results[0].Text != "User's name is Greg Buchenberger" {
		t.Errorf("expected updated text, got %q", results[0].Text)
	}
}

func TestDB_UpdateMemory_NonExistentReturnsError(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	err = db.UpdateMemory("nonexistent", "new text")
	if !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("UpdateMemory(nonexistent) = %v, want ErrMemoryNotFound", err)
	}
}

func TestDB_EndSession(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.CreateSession(Session{
		ID: "sess-1", StartedAt: 1000, CWD: "/tmp", Model: "gpt-4o",
	})

	if err := db.EndSession("sess-1", 2000, 500, 300, 120, 200, 100); err != nil {
		t.Fatalf("EndSession() error: %v", err)
	}

	sessions, _ := db.ListSessions(10)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].EndedAt != 2000 {
		t.Errorf("expected ended_at=2000, got %d", sessions[0].EndedAt)
	}
	if sessions[0].TokensIn != 500 {
		t.Errorf("expected tokens_in=500, got %d", sessions[0].TokensIn)
	}
	if sessions[0].TokensOut != 300 {
		t.Errorf("expected tokens_out=300, got %d", sessions[0].TokensOut)
	}
	if sessions[0].TokensCached != 120 {
		t.Errorf("expected tokens_cached=120, got %d", sessions[0].TokensCached)
	}
	if sessions[0].SubTokensIn != 200 {
		t.Errorf("expected sub_tokens_in=200, got %d", sessions[0].SubTokensIn)
	}
	if sessions[0].SubTokensOut != 100 {
		t.Errorf("expected sub_tokens_out=100, got %d", sessions[0].SubTokensOut)
	}
}

func TestDB_UpdateSessionUsage(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.CreateSession(Session{
		ID: "sess-2", StartedAt: 1000, CWD: "/tmp", Model: "m",
	})

	// The incremental flush: usage lands without ending the session.
	if err := db.UpdateSessionUsage("sess-2", 100, 20, 60, 0, 0); err != nil {
		t.Fatalf("UpdateSessionUsage() error: %v", err)
	}

	s, err := db.GetSession("sess-2")
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if s.TokensIn != 100 || s.TokensOut != 20 || s.TokensCached != 60 {
		t.Errorf("usage flush: got in=%d out=%d cached=%d, want 100/20/60", s.TokensIn, s.TokensOut, s.TokensCached)
	}
	if s.EndedAt != 0 {
		t.Errorf("usage flush must not end the session, got ended_at=%d", s.EndedAt)
	}

	// A later flush overwrites with the newer running totals.
	if err := db.UpdateSessionUsage("sess-2", 300, 40, 200, 150, 10); err != nil {
		t.Fatalf("UpdateSessionUsage() (2) error: %v", err)
	}
	s, err = db.GetSession("sess-2")
	if err != nil {
		t.Fatalf("GetSession() (2) error: %v", err)
	}
	if s.TokensIn != 300 || s.TokensOut != 40 || s.TokensCached != 200 || s.SubTokensIn != 150 || s.SubTokensOut != 10 {
		t.Errorf("usage flush (2): got %+v", s)
	}
}

func TestDB_SearchMemoryByTag(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.AddMemory(Entry{
		ID: "mem-1", Text: "User prefers dark mode", Tags: `["preferences"]`,
		Source: "agent", CreatedAt: 1,
	})
	db.AddMemory(Entry{
		ID: "mem-2", Text: "Project uses Go", Tags: `["tech"]`,
		Source: "agent", CreatedAt: 2,
	})
	db.AddMemory(Entry{
		ID: "mem-3", Text: "User's name is Greg", Tags: `["user_info"]`,
		Source: "agent", CreatedAt: 3,
	})

	t.Run("filter by tag returns only matching", func(t *testing.T) {
		results, _ := db.SearchMemory("", 10, "preferences")
		if len(results) != 1 {
			t.Fatalf("expected 1 result with tag filter, got %d", len(results))
		}
		if results[0].Text != "User prefers dark mode" {
			t.Errorf("wrong result: %q", results[0].Text)
		}
	})

	t.Run("empty tag returns all", func(t *testing.T) {
		results, _ := db.SearchMemory("", 10, "")
		if len(results) != 3 {
			t.Errorf("expected 3 results, got %d", len(results))
		}
	})

	t.Run("query plus tag filter", func(t *testing.T) {
		results, _ := db.SearchMemory("Greg", 10, "user_info")
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].ID != "mem-3" {
			t.Errorf("wrong result id: %q", results[0].ID)
		}
	})
}

func TestDB_SearchMemory_TagNotFound(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.AddMemory(Entry{
		ID: "mem-1", Text: "User prefers dark mode", Tags: `["preferences"]`,
		Source: "agent", CreatedAt: 1,
	})

	results, _ := db.SearchMemory("", 10, "nonexistent")
	if len(results) != 0 {
		t.Errorf("expected 0 results for non-matching tag, got %d", len(results))
	}
}

func TestDB_CreateSession_storesSystemPrompt(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.CreateSession(Session{
		ID:           "sess-1",
		StartedAt:    1000,
		CWD:          "/tmp",
		Model:        "gpt-4o",
		SystemPrompt: "You are a helpful assistant.",
	})

	s, err := db.GetSession("sess-1")
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if s.SystemPrompt != "You are a helpful assistant." {
		t.Errorf("SystemPrompt = %q, want %q", s.SystemPrompt, "You are a helpful assistant.")
	}
}

func TestDB_UpdateSessionSummary_persistsCorrectly(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.CreateSession(Session{ID: "sess-1", StartedAt: 1000})

	if err := db.UpdateSessionSummary("sess-1", "Compacted context about file changes."); err != nil {
		t.Fatalf("UpdateSessionSummary() error: %v", err)
	}

	s, err := db.GetSession("sess-1")
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if s.CompactedSummary != "Compacted context about file changes." {
		t.Errorf("CompactedSummary = %q, want %q", s.CompactedSummary, "Compacted context about file changes.")
	}
}

func TestDB_ListSessions_returnsNewFields(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	db.CreateSession(Session{
		ID: "sess-1", StartedAt: 1000, SystemPrompt: "System A",
	})
	db.UpdateSessionSummary("sess-1", "summary-A")

	sessions, err := db.ListSessions(10)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].SystemPrompt != "System A" {
		t.Errorf("SystemPrompt = %q, want %q", sessions[0].SystemPrompt, "System A")
	}
	if sessions[0].CompactedSummary != "summary-A" {
		t.Errorf("CompactedSummary = %q, want %q", sessions[0].CompactedSummary, "summary-A")
	}
}

func TestDB_GetSession_nonexistentReturnsError(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	_, err = db.GetSession("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

// stubEmbedder returns fixed embeddings for testing.
type stubEmbedder struct{}

func (s stubEmbedder) Embed(_ context.Context, text string) (Embedding, error) {
	// Simple hash: length determines vector values.
	return Embedding{float32(len(text)) / 100, 0.5, -float32(len(text)) / 200}, nil
}

// failingEmbedder always fails, standing in for a down or unreachable
// embed server (GitHub #311).
type failingEmbedder struct{ err error }

func (f failingEmbedder) Embed(context.Context, string) (Embedding, error) {
	return nil, f.err
}

func TestDB_SearchMemoryVector(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	db.SetEmbedder(stubEmbedder{})

	e1 := Entry{ID: "v1", Text: "Postgres connection pooling", Source: "agent", CreatedAt: 1}
	e2 := Entry{ID: "v2", Text: "database setup", Source: "agent", CreatedAt: 2}
	e3 := Entry{ID: "v3", Text: "unrelated topic about llamas", Source: "agent", CreatedAt: 3}
	addMemoryAndWait(t, db, e1)
	addMemoryAndWait(t, db, e2)
	addMemoryAndWait(t, db, e3)

	results, err := db.SearchMemoryVector(context.Background(), "sql database", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected vector results")
	}
	if len(results) > 2 {
		t.Fatalf("got %d results, want <= 2", len(results))
	}
	// "llamas" should score lowest (different text -> embedding math differs most)
	if results[0].ID == "v3" {
		t.Errorf("expected database-related entry first, got %q", results[0].ID)
	}
}

func TestDB_SearchMemoryVectorNoEmbedder(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	db.AddMemory(Entry{ID: "x1", Text: "test", Source: "agent", CreatedAt: 1})
	results, err := db.SearchMemoryVector(context.Background(), "query", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty when embedder is nil, got %d results", len(results))
	}
}

func TestDB_ReconcileEmbeddings(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Insert entries WITHOUT embedder — they'll have NULL embedding.
	db.AddMemory(Entry{ID: "r1", Text: "reconcile me", Source: "agent", CreatedAt: 1})
	db.AddMemory(Entry{ID: "r2", Text: "reconcile me too", Source: "agent", CreatedAt: 2})

	// Now set the embedder and reconcile.
	db.SetEmbedder(stubEmbedder{})
	updated, err := db.ReconcileEmbeddings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("ReconcileEmbeddings updated %d, want 2", updated)
	}

	// Verify they were embedded by searching.
	results, err := db.SearchMemoryVector(context.Background(), "test", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 vector results, got %d", len(results))
	}
}

func TestDB_Migration_AddsEmbeddingColumn(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.db")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Re-open — the migration should be idempotent.
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	db2.SetEmbedder(stubEmbedder{})
	addMemoryAndWait(t, db2, Entry{ID: "m1", Text: "after migration", Source: "agent", CreatedAt: 1})

	results, err := db2.SearchMemoryVector(context.Background(), "migration", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result after migration, got %d", len(results))
	}
}

func TestDB_SearchMessagesVector(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	db.SetEmbedder(stubEmbedder{})

	// Create a session
	sid := "test-session"
	_, err = db.sql.Exec(`INSERT INTO sessions (id, started_at) VALUES (?, ?)`, sid, 1)
	if err != nil {
		t.Fatal(err)
	}

	addMessageAndWait(t, db, Message{SessionID: sid, Idx: 0, Role: "user", Content: "how to pool DB connections", Timestamp: 1, ID: "m1"})
	addMessageAndWait(t, db, Message{SessionID: sid, Idx: 1, Role: "assistant", Content: "use PgBouncer", Timestamp: 2, ID: "m2"})
	addMessageAndWait(t, db, Message{SessionID: sid, Idx: 2, Role: "tool", Content: "tool output", Timestamp: 3, ID: "m3"})
	addMessageAndWait(t, db, Message{SessionID: sid, Idx: 3, Role: "user", Content: "llamas are great", Timestamp: 4, ID: "m4"})

	results, err := db.SearchMessagesVector(context.Background(), "database connection", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if len(results) > 2 {
		t.Fatalf("got %d, want <= 2", len(results))
	}
	// Tool messages are not embedded, so "m3" should not appear.
	for _, r := range results {
		if r.ID == "m3" {
			t.Error("tool messages should not have embeddings")
		}
	}
}

func addMemoryAndWait(t *testing.T, db *DB, e Entry) {
	t.Helper()
	if err := db.AddMemory(e); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if ch := db.EmbedMemoryAsync(e.ID, e.Text); ch != nil {
		<-ch
	}
}

func addMessageAndWait(t *testing.T, db *DB, m Message) {
	t.Helper()
	if err := db.AddMessage(m); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if db.Embedder() == nil {
		return
	}
	if m.Role != "user" && m.Role != "assistant" {
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var emb []byte
		db.sql.QueryRow(`SELECT COALESCE(embedding, '') FROM messages WHERE id = ?`, m.ID).Scan(&emb)
		if len(emb) > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Final check: the embedding may have landed during the last sleep.
	var emb []byte
	db.sql.QueryRow(`SELECT COALESCE(embedding, '') FROM messages WHERE id = ?`, m.ID).Scan(&emb)
	if len(emb) > 0 {
		return
	}
	t.Fatalf("timed out waiting for embedding of message %s", m.ID)
}

func TestDB_SearchMessagesVectorNoEmbedder(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.sql.Exec(`INSERT INTO sessions (id, started_at) VALUES ('s1', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	db.AddMessage(Message{SessionID: "s1", Idx: 0, Role: "user", Content: "test", Timestamp: 1, ID: "x1"})
	results, err := db.SearchMessagesVector(context.Background(), "query", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty when embedder is nil, got %d results", len(results))
	}
}

func TestDB_SQLiteDSN(t *testing.T) {
	want := "/tmp/a.db?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	if got := sqliteDSN("/tmp/a.db"); got != want {
		t.Errorf("sqliteDSN(plain) = %q, want %q", got, want)
	}
	wantQ := "/tmp/a.db?cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	if got := sqliteDSN("/tmp/a.db?cache=shared"); got != wantQ {
		t.Errorf("sqliteDSN(existing query) = %q, want %q", got, wantQ)
	}
}

func TestDB_PragmasApplied(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var fk int
	if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}

	var bt int
	if err := db.sql.QueryRow(`PRAGMA busy_timeout`).Scan(&bt); err != nil {
		t.Fatal(err)
	}
	if bt != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", bt)
	}
}

func TestDB_ForeignKeysEnforced(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	m := Message{SessionID: "missing-session", Idx: 0, Role: "user", Content: "hi", Timestamp: 1, ID: "m1"}
	if err := db.AddMessage(m); err == nil {
		t.Fatal("expected foreign key error for message with no parent session")
	}
}

func TestDB_AddMessage_IdempotentOnSamePosition(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.CreateSession(Session{ID: "sess-1", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}

	m := Message{SessionID: "sess-1", Idx: 0, Role: "user", Content: "hello", Timestamp: 1, ID: "m1"}
	if err := db.AddMessage(m); err != nil {
		t.Fatalf("first AddMessage: %v", err)
	}
	if err := db.AddMessage(m); err != nil {
		t.Fatalf("second AddMessage (idempotent) should not error: %v", err)
	}

	msgs, err := db.GetMessages("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message after duplicate insert, got %d", len(msgs))
	}
}

func TestDB_AddMessage_ConflictWithDifferentContent(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.CreateSession(Session{ID: "sess-1", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}

	if err := db.AddMessage(Message{SessionID: "sess-1", Idx: 0, Role: "user", Content: "hello", Timestamp: 1, ID: "id-a"}); err != nil {
		t.Fatalf("first AddMessage: %v", err)
	}
	// A different message at the same position must not be silently dropped.
	err = db.AddMessage(Message{SessionID: "sess-1", Idx: 0, Role: "user", Content: "world", Timestamp: 2, ID: "id-b"})
	if err == nil {
		t.Fatal("expected conflict error for different content at the same position")
	}

	msgs, err := db.GetMessages("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Errorf("expected first write to win, got %q", msgs[0].Content)
	}
}

func TestDB_AddMemoryDedup_ExactOnly(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.AddMemory(Entry{ID: "mem-1", Text: "User prefers dark mode", Source: "agent", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	// Near-duplicate (trailing punctuation) must NOT be deduped: dedup is exact.
	dupID, err := db.AddMemoryDedup(Entry{ID: "mem-2", Text: "User prefers dark mode!", Source: "agent", CreatedAt: 2})
	if err != nil {
		t.Fatal(err)
	}
	if dupID != "" {
		t.Errorf("near-duplicate text should not be deduped, got %q", dupID)
	}
	all, _ := db.ListMemory(10)
	if len(all) != 2 {
		t.Errorf("expected 2 memories, got %d", len(all))
	}
}

func TestDB_UpdateMemory_RecomputesDigest(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.AddMemory(Entry{ID: "mem-1", Text: "alpha", Source: "agent", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMemory(Entry{ID: "mem-2", Text: "beta", Source: "agent", CreatedAt: 2}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMemory("mem-2", "gamma"); err != nil {
		t.Fatal(err)
	}

	dupID, err := db.AddMemoryDedup(Entry{ID: "mem-3", Text: "gamma", Source: "agent", CreatedAt: 3})
	if err != nil {
		t.Fatal(err)
	}
	if dupID != "mem-2" {
		t.Errorf("expected dedup against updated mem-2, got %q", dupID)
	}
}

func TestDB_ReconcileMemoryDigests(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.sql.Exec(`INSERT INTO memory (id, text, tags, source, created_at) VALUES ('legacy-1', 'legacy text', NULL, 'agent', 1)`); err != nil {
		t.Fatal(err)
	}

	n, err := db.ReconcileMemoryDigests()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 backfilled row, got %d", n)
	}

	var digest string
	if err := db.sql.QueryRow(`SELECT digest FROM memory WHERE id = 'legacy-1'`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != memoryDigest("legacy text") {
		t.Errorf("digest = %q, want %q", digest, memoryDigest("legacy text"))
	}
}

// TestNewEntryIDIsUnique pins the ID contract the old mem-<UnixNano> form
// broke: two IDs minted back to back must never collide, because the clock's
// nanosecond resolution is coarse enough on some platforms for consecutive
// calls to return the same tick — a primary-key collision, not a near miss.
func TestNewEntryIDIsUnique(t *testing.T) {
	seen := make(map[string]bool, 256)
	for i := 0; i < 256; i++ {
		id := NewEntryID()
		if !strings.HasPrefix(id, "mem-") {
			t.Fatalf("id %q lost the mem- prefix", id)
		}
		if seen[id] {
			t.Fatalf("id collision at %d: %q", i, id)
		}
		seen[id] = true
	}
}

// TestAddMemoryDedup_RapidDistinctInsertsBothPersist reproduces the flaky
// failure seen in TestMemoryAddTool_DedupAllowsUniqueText at the layer that
// owns the constraint: rapid inserts of distinct texts must all persist.
func TestAddMemoryDedup_RapidDistinctInsertsBothPersist(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for i := 0; i < 50; i++ {
		for _, suffix := range []string{"a", "b"} {
			e := Entry{
				ID:        NewEntryID(),
				Text:      fmt.Sprintf("fact %d %s", i, suffix),
				Source:    "test",
				CreatedAt: time.Now().Unix(),
			}
			if _, err := db.AddMemoryDedup(e); err != nil {
				t.Fatalf("AddMemoryDedup(%q): %v", e.Text, err)
			}
		}
	}

	all, err := db.ListMemory(1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 100 {
		t.Errorf("expected 100 memories, got %d", len(all))
	}
}

func TestDB_SessionWorkspaceMetadata(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer db.Close()

	// D4 of the isolated-workspace plan: the backend and the omitted
	// tool names are part of the session record, so a reader of two
	// runs can tell "didn't use" from "couldn't use".
	db.CreateSession(Session{ID: "sess-ws", StartedAt: 1000})
	if err := db.UpdateSessionWorkspace("sess-ws", "containerd", "background_process,go_refactor,role,bisect"); err != nil {
		t.Fatalf("UpdateSessionWorkspace() error: %v", err)
	}

	s, err := db.GetSession("sess-ws")
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if s.WorkspaceBackend != "containerd" {
		t.Errorf("WorkspaceBackend = %q, want %q", s.WorkspaceBackend, "containerd")
	}
	if s.WorkspaceOmittedTools != "background_process,go_refactor,role,bisect" {
		t.Errorf("WorkspaceOmittedTools = %q", s.WorkspaceOmittedTools)
	}

	// The metadata flows through the listing surface too.
	sessions, err := db.ListSessions(10)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].WorkspaceBackend != "containerd" {
		t.Errorf("ListSessions workspace metadata = %+v", sessions)
	}

	// A second update overwrites: the columns describe the session's
	// most recent run, so a resume that changed mode rewrites them.
	if err := db.UpdateSessionWorkspace("sess-ws", "local", ""); err != nil {
		t.Fatalf("UpdateSessionWorkspace() error: %v", err)
	}
	s, _ = db.GetSession("sess-ws")
	if s.WorkspaceBackend != "local" || s.WorkspaceOmittedTools != "" {
		t.Errorf("after update: backend = %q, omitted = %q, want local/empty", s.WorkspaceBackend, s.WorkspaceOmittedTools)
	}
}

func TestDB_SessionWorkspaceMetadata_MigratesExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	// Create a database with the pre-D4 sessions schema directly, so the
	// migration under test is real: the first Open of a fresh file would
	// create the table with the new columns already present and prove
	// nothing (same discipline as TestMigrate_AddsCrossLinkColumns).
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	legacySchema := `
CREATE TABLE sessions (
	id TEXT PRIMARY KEY,
	started_at INTEGER NOT NULL,
	ended_at INTEGER,
	cwd TEXT,
	model TEXT,
	tokens_in INTEGER DEFAULT 0,
	tokens_out INTEGER DEFAULT 0,
	system_prompt TEXT DEFAULT ''
);
INSERT INTO sessions (id, started_at, cwd, model) VALUES ('sess-old', 1000, '/tmp', '');
`
	if _, err := legacy.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Open: the ALTER-based migration must add the workspace columns to
	// the existing table.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	defer db.Close()

	// The old row reads back with defaults before anything writes it.
	s, err := db.GetSession("sess-old")
	if err != nil {
		t.Fatalf("GetSession after migration: %v", err)
	}
	if s.WorkspaceBackend != "" || s.WorkspaceOmittedTools != "" {
		t.Errorf("pre-existing row must default to empty metadata, got %q/%q", s.WorkspaceBackend, s.WorkspaceOmittedTools)
	}

	if err := db.UpdateSessionWorkspace("sess-old", "containerd", "role"); err != nil {
		t.Fatalf("UpdateSessionWorkspace after migration: %v", err)
	}
	s, err = db.GetSession("sess-old")
	if err != nil {
		t.Fatalf("GetSession after write: %v", err)
	}
	if s.WorkspaceBackend != "containerd" || s.WorkspaceOmittedTools != "role" {
		t.Errorf("post-migration metadata = %q/%q", s.WorkspaceBackend, s.WorkspaceOmittedTools)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Embedding failure surfacing (GitHub #311): every swallow point must
// log a rate-limited warning and degrade gracefully without panicking.
// ─────────────────────────────────────────────────────────────────────

func TestDB_EmbedMemoryAsync_FailureSurfaced(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)
	db.SetEmbedder(failingEmbedder{errors.New("embed server down")})

	if err := db.AddMemory(Entry{ID: "m1", Text: "remember this", Source: "agent", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	ch := db.EmbedMemoryAsync("m1", "remember this")
	if ch == nil {
		t.Fatal("expected done channel with embedder configured")
	}
	<-ch

	out := logs.String()
	if !strings.Contains(out, "embedding failed") {
		t.Errorf("expected warning log, got: %q", out)
	}
	if !strings.Contains(out, "embed_memory") {
		t.Errorf("expected op name in log, got: %q", out)
	}
	if !strings.Contains(out, "embed server down") {
		t.Errorf("expected error in log, got: %q", out)
	}

	var blob []byte
	if err := db.sql.QueryRow(`SELECT COALESCE(embedding, '') FROM memory WHERE id = 'm1'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if len(blob) != 0 {
		t.Errorf("failed embed must leave embedding NULL, got %d bytes", len(blob))
	}
}

func TestDB_SearchMemoryVector_EmbedFailureDegradesToNil(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)
	db.SetEmbedder(failingEmbedder{errors.New("connection refused")})

	results, err := db.SearchMemoryVector(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("SearchMemoryVector must degrade, got error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results on embed failure, got %d", len(results))
	}

	out := logs.String()
	if !strings.Contains(out, "search_memory_vector") {
		t.Errorf("expected op name in log, got: %q", out)
	}
	if !strings.Contains(out, "connection refused") {
		t.Errorf("expected error in log, got: %q", out)
	}
}

func TestDB_SearchMemoryVectorScopedDiag_SurfacesEmbedError(t *testing.T) {
	_ = captureWarnLogs(t)
	db := openTestDB(t)
	db.SetEmbedder(failingEmbedder{errors.New("connection refused")})

	results, err := db.SearchMemoryVectorScopedDiag(context.Background(), "anything", 5, ScopeAll)
	if err == nil {
		t.Fatal("Diag must return the embed error instead of degrading silently")
	}
	if results != nil {
		t.Fatalf("expected nil results on embed failure, got %d", len(results))
	}
	var embedErr *EmbedError
	if !errors.As(err, &embedErr) {
		t.Fatalf("error %v is not an *EmbedError", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("diag error lost the cause: %v", err)
	}
}

func TestDB_SearchMemoryVectorScopedDiag_NoEmbedder(t *testing.T) {
	db := openTestDB(t)
	results, err := db.SearchMemoryVectorScopedDiag(context.Background(), "query", 5, ScopeAll)
	if err != nil || results != nil {
		t.Fatalf("no embedder = (%v, %v), want (nil, nil)", results, err)
	}
}

func TestDB_ReconcileEmbeddings_EmbedFailureSkipsRow(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)

	// Insert without an embedder so the rows have NULL embeddings.
	if err := db.AddMemory(Entry{ID: "r1", Text: "reconcile me", Source: "agent", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	db.SetEmbedder(failingEmbedder{errors.New("timeout")})

	updated, err := db.ReconcileEmbeddings(context.Background())
	if err != nil {
		t.Fatalf("ReconcileEmbeddings must not fail on embed errors: %v", err)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0", updated)
	}

	out := logs.String()
	if !strings.Contains(out, "reconcile_embeddings") {
		t.Errorf("expected op name in log, got: %q", out)
	}
	if !strings.Contains(out, "timeout") {
		t.Errorf("expected error in log, got: %q", out)
	}

	var blob []byte
	if err := db.sql.QueryRow(`SELECT COALESCE(embedding, '') FROM memory WHERE id = 'r1'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if len(blob) != 0 {
		t.Error("failed reconcile must leave embedding NULL")
	}
}

// TestDB_DeleteMemory_NotFound pins the #328 fix: a delete matching no
// rows must report "memory not found" instead of silently succeeding —
// the fake success let truncated-ID deletes diverge the DB and
// duplicates accumulate.
func TestDB_DeleteMemory_NotFound(t *testing.T) {
	db := openTestDB(t)
	err := db.DeleteMemory("mem-0000000000000000000000000000dead")
	if !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("DeleteMemory(nonexistent) = %v, want a memory-not-found error", err)
	}
}

// TestDB_DeleteMemory_RemovesRow covers the happy path and the
// consume-once behavior of the new RowsAffected check.
func TestDB_DeleteMemory_RemovesRow(t *testing.T) {
	db := openTestDB(t)
	e := Entry{ID: NewEntryID(), Text: "deletable fact", Source: "agent", CreatedAt: time.Now().Unix()}
	if dupID, err := db.AddMemoryDedup(e); err != nil || dupID != "" {
		t.Fatalf("AddMemoryDedup: dup=%q err=%v", dupID, err)
	}
	if err := db.DeleteMemory(e.ID); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	if _, err := db.GetMemory(e.ID); err == nil {
		t.Error("GetMemory after delete must fail")
	}
	if err := db.DeleteMemory(e.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("second DeleteMemory = %v, want memory-not-found", err)
	}
}
