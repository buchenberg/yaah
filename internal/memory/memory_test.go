package memory

import (
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
