package memory

import (
	"strings"
	"testing"
)

// TestMigrate_rejectsNewerSchema pins the fail-fast rule: a database
// stamped by a newer yaah must be refused at open, not silently
// queried with half-supported SQL (review A6).
func TestMigrate_rejectsNewerSchema(t *testing.T) {
	path := t.TempDir() + "/future.db"

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.sql.Exec(`UPDATE schema_meta SET value = '999' WHERE key = 'version'`); err != nil {
		t.Fatalf("stamp version: %v", err)
	}
	db.Close()

	if _, err := Open(path); err == nil {
		t.Fatal("Open on a newer-schema database should fail")
	} else if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Fatalf("error = %v, want newer-schema message", err)
	}
}

// TestCreateSession_capsSystemPrompt pins the system_prompt cap
// (review A6/S7): unbounded prompts are truncated with a marker.
func TestCreateSession_capsSystemPrompt(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	big := strings.Repeat("x", maxSessionPromptBytes+4096)
	s := Session{ID: "sess-cap", StartedAt: 1, SystemPrompt: big}
	if err := db.CreateSession(s); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := db.GetSession("sess-cap")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(got.SystemPrompt) >= len(big) {
		t.Fatalf("system_prompt not truncated: %d bytes", len(got.SystemPrompt))
	}
	if !strings.HasSuffix(got.SystemPrompt, "[truncated]") {
		t.Errorf("truncated prompt missing marker: %q", got.SystemPrompt[len(got.SystemPrompt)-30:])
	}
}

// TestUpdateSessionSummary_capsSummary pins the compacted_summary cap.
func TestUpdateSessionSummary_capsSummary(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.CreateSession(Session{ID: "sess-sum", StartedAt: 1}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	big := strings.Repeat("y", maxSessionPromptBytes+4096)
	if err := db.UpdateSessionSummary("sess-sum", big); err != nil {
		t.Fatalf("UpdateSessionSummary: %v", err)
	}
	got, err := db.GetSession("sess-sum")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(got.CompactedSummary) >= len(big) {
		t.Fatalf("compacted_summary not truncated: %d bytes", len(got.CompactedSummary))
	}
	if !strings.HasSuffix(got.CompactedSummary, "[truncated]") {
		t.Errorf("truncated summary missing marker")
	}
}

// TestSearchMemory_batchesAccessBump verifies that searching still
// bumps access counts after the batched-update change (review A6).
func TestSearchMemory_batchesAccessBump(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	id, err := db.AddMemoryDedup(Entry{Text: "unique searchable zebra fact", Tags: `["t"]`})
	if err != nil && id == "" {
		t.Fatalf("AddMemoryDedup: %v", err)
	}
	if _, err := db.SearchMemory("zebra", 10); err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	e, err := db.GetMemory(id)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if e.AccessCount != 1 {
		t.Errorf("access_count = %d, want 1 after one search", e.AccessCount)
	}
}
