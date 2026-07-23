package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func msg(id, role string, idx int) Message {
	return Message{
		ID:        id,
		SessionID: "ses-test",
		Idx:       idx,
		Role:      role,
		Content:   "test content",
		Timestamp: time.Now().Unix(),
	}
}

func TestDebouncedWriter_Coalescing(t *testing.T) {
	db := openTestDB(t)
	dw := NewDebouncedWriter(db)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		m := msg("msg-"+string(rune('a'+i)), "assistant", i)
		if err := dw.Update(ctx, m); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}

	dw.Flush()

	msgs, err := db.GetMessages("ses-test")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 5 {
		t.Errorf("expected 5 messages, got %d", len(msgs))
	}
}

func TestDebouncedWriter_UserMessageFlushes(t *testing.T) {
	db := openTestDB(t)
	dw := NewDebouncedWriter(db)

	ctx := context.Background()
	if err := dw.Update(ctx, msg("a1", "assistant", 1)); err != nil {
		t.Fatalf("Update assistant: %v", err)
	}
	if err := dw.Update(ctx, msg("a2", "assistant", 2)); err != nil {
		t.Fatalf("Update assistant: %v", err)
	}
	if err := dw.Update(ctx, msg("u1", "user", 3)); err != nil {
		t.Fatalf("Update user: %v", err)
	}

	msgs, err := db.GetMessages("ses-test")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) < 3 {
		t.Errorf("expected at least 3 messages, got %d", len(msgs))
	}
}

func TestDebouncedWriter_FlushDrainsPending(t *testing.T) {
	db := openTestDB(t)
	dw := NewDebouncedWriter(db)

	ctx := context.Background()
	if err := dw.Update(ctx, msg("a1", "assistant", 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := dw.Update(ctx, msg("a2", "assistant", 1)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := dw.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	msgs, err := db.GetMessages("ses-test")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages after Flush, got %d", len(msgs))
	}
}
