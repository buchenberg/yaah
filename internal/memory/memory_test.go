package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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

	// Deleting again should not error (no-op)
	if err := db.DeleteMemory("mem-1"); err != nil {
		t.Errorf("DeleteMemory() on non-existent should not error: %v", err)
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
	if err == nil {
		t.Error("expected error for non-existent memory update")
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

	if err := db.EndSession("sess-1", 2000, 500, 300); err != nil {
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
	db.AddMemory(e1)
	db.AddMemory(e2)
	db.AddMemory(e3)

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
	db2.AddMemory(Entry{ID: "m1", Text: "after migration", Source: "agent", CreatedAt: 1})

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

	db.AddMessage(Message{SessionID: sid, Idx: 0, Role: "user", Content: "how to pool DB connections", Timestamp: 1, ID: "m1"})
	db.AddMessage(Message{SessionID: sid, Idx: 1, Role: "assistant", Content: "use PgBouncer", Timestamp: 2, ID: "m2"})
	db.AddMessage(Message{SessionID: sid, Idx: 2, Role: "tool", Content: "tool output", Timestamp: 3, ID: "m3"})
	db.AddMessage(Message{SessionID: sid, Idx: 3, Role: "user", Content: "llamas are great", Timestamp: 4, ID: "m4"})

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
