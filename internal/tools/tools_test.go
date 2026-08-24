package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
)

func newTestDB(t *testing.T) *memory.DB {
	t.Helper()
	db, err := memory.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMemoryDeleteTool_DeletesEntry(t *testing.T) {
	db := newTestDB(t)
	add := &MemoryAddTool{DB: db}
	del := &MemoryDeleteTool{DB: db}

	add.Execute(context.Background(), `{"text":"User's name is Greg"}`)
	results, _ := db.ListMemory(10)
	if len(results) != 1 {
		t.Fatalf("expected 1 memory after add, got %d", len(results))
	}

	result, err := del.Execute(context.Background(), `{"id":"`+results[0].ID+`"}`)
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	t.Logf("delete result: %s", result)

	all, _ := db.ListMemory(10)
	if len(all) != 0 {
		t.Errorf("expected 0 memories after delete, got %d", len(all))
	}
}

func TestMemoryUpdateTool_UpdatesEntry(t *testing.T) {
	db := newTestDB(t)
	add := &MemoryAddTool{DB: db}
	upd := &MemoryUpdateTool{DB: db}
	search := &MemorySearchTool{DB: db}

	add.Execute(context.Background(), `{"text":"User's name is Greg"}`)
	results, _ := db.ListMemory(10)
	if len(results) != 1 {
		t.Fatalf("expected 1 memory after add, got %d", len(results))
	}

	updArgs := `{"id":"` + results[0].ID + `","text":"User's name is Greg Buchenberger"}`
	result, err := upd.Execute(context.Background(), updArgs)
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	t.Logf("update result: %s", result)

	searchResult, _ := search.Execute(context.Background(), `{"query":"Buchenberger"}`)
	if searchResult == "No matching memories found." {
		t.Error("expected to find updated memory text")
	}
}

func TestMemoryAddTool_DedupSkipsDuplicates(t *testing.T) {
	db := newTestDB(t)
	add := &MemoryAddTool{DB: db}

	result1, err := add.Execute(context.Background(), `{"text":"The user prefers dark mode"}`)
	if err != nil {
		t.Fatalf("first add error: %v", err)
	}
	if result1[:6] != "Memory" {
		t.Fatalf("unexpected result: %s", result1)
	}

	result2, err := add.Execute(context.Background(), `{"text":"The user prefers dark mode"}`)
	if err != nil {
		t.Fatalf("second add error: %v", err)
	}
	if result2[:6] != "Memory" {
		t.Fatalf("unexpected result: %s", result2)
	}

	all, _ := db.ListMemory(10)
	if len(all) != 1 {
		t.Errorf("expected 1 memory after dedup, got %d", len(all))
	}
}

func TestMemoryAddTool_DedupAllowsUniqueText(t *testing.T) {
	db := newTestDB(t)
	add := &MemoryAddTool{DB: db}

	if _, err := add.Execute(context.Background(), `{"text":"User's name is Greg"}`); err != nil {
		t.Fatalf("first add error: %v", err)
	}
	if _, err := add.Execute(context.Background(), `{"text":"Project uses Go for backend"}`); err != nil {
		t.Fatalf("second add error: %v", err)
	}

	all, _ := db.ListMemory(10)
	if len(all) != 2 {
		t.Errorf("expected 2 memories, got %d", len(all))
	}
}

func TestMemorySearchTool_TracksAccess(t *testing.T) {
	db := newTestDB(t)
	add := &MemoryAddTool{DB: db}
	search := &MemorySearchTool{DB: db}

	add.Execute(context.Background(), `{"text":"the user prefers dark mode"}`)
	results1, _ := db.ListMemory(10)
	if results1[0].AccessCount != 0 {
		t.Errorf("fresh memory should have access_count 0, got %d", results1[0].AccessCount)
	}

	search.Execute(context.Background(), `{"query":"dark mode"}`)
	results2, _ := db.ListMemory(10)
	if results2[0].AccessCount != 1 {
		t.Errorf("after search, access_count should be 1, got %d", results2[0].AccessCount)
	}
}
