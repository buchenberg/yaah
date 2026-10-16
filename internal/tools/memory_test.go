package tools

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
)

// memoryIDPattern matches a full entry ID (mem- + 32 hex) — the form
// every memory tool result must surface verbatim so callers can pass it
// back (#328).
var memoryIDPattern = regexp.MustCompile(`mem-[0-9a-f]{32}`)

func newMemoryToolDB(t *testing.T) *memory.DB {
	t.Helper()
	db, err := memory.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestMemoryTools_FullIDRoundTrip pins the #328 acceptance criteria:
// the ID surfaced by memory_add works verbatim in memory_update and
// memory_delete, and confirmations echo the full ID.
func TestMemoryTools_FullIDRoundTrip(t *testing.T) {
	db := newMemoryToolDB(t)
	ctx := context.Background()

	addRes, err := (&MemoryAddTool{DB: db}).Execute(ctx, `{"text":"round trip fact"}`)
	if err != nil {
		t.Fatalf("memory_add: %v", err)
	}
	id := memoryIDPattern.FindString(addRes)
	if id == "" {
		t.Fatalf("memory_add must surface the full 36-char ID: %q", addRes)
	}

	if _, err := (&MemoryUpdateTool{DB: db}).Execute(ctx, `{"id":"`+id+`","text":"updated fact"}`); err != nil {
		t.Errorf("memory_update with the surfaced ID must succeed: %v", err)
	}

	delRes, err := (&MemoryDeleteTool{DB: db}).Execute(ctx, `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatalf("memory_delete with the surfaced ID must succeed: %v", err)
	}
	if !strings.Contains(delRes, id) {
		t.Errorf("delete confirmation must echo the full ID: %q", delRes)
	}
}

// TestMemoryTools_TruncatedIDFailsLoudly pins the fake-success half of
// #328: a truncated (legacy-surfaced) ID must error on delete, never
// report success, and the row must survive untouched.
func TestMemoryTools_TruncatedIDFailsLoudly(t *testing.T) {
	db := newMemoryToolDB(t)
	ctx := context.Background()

	addRes, err := (&MemoryAddTool{DB: db}).Execute(ctx, `{"text":"truncation probe"}`)
	if err != nil {
		t.Fatalf("memory_add: %v", err)
	}
	id := memoryIDPattern.FindString(addRes)
	if id == "" {
		t.Fatalf("no full ID in add result: %q", addRes)
	}
	truncated := id[:12]

	if res, err := (&MemoryDeleteTool{DB: db}).Execute(ctx, `{"id":"`+truncated+`"}`); err == nil {
		t.Errorf("memory_delete with a truncated ID must fail, got result %q", res)
	} else if !strings.Contains(err.Error(), "memory not found") {
		t.Errorf("delete error = %v, want memory-not-found", err)
	}

	if _, err := db.GetMemory(id); err != nil {
		t.Errorf("row lost after a failed truncated delete: %v", err)
	}
}
