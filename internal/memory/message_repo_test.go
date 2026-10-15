package memory

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestDB_DeleteMessagesFrom pins the range-delete used by compaction
// rebasing and turn restore (review finding B7): only rows at idx >=
// fromIdx of the named session are removed, other sessions untouched.
func TestDB_DeleteMessagesFrom(t *testing.T) {
	db := openTestDB(t)

	for i := 0; i < 5; i++ {
		if err := db.AddMessage(msg(idAt(i), "user", i)); err != nil {
			t.Fatalf("AddMessage idx %d: %v", i, err)
		}
	}
	// A second session whose rows must survive.
	if err := db.CreateSession(Session{ID: "other", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMessage(Message{ID: "other-0", SessionID: "other", Idx: 0, Role: "user", Content: "keep", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteMessagesFrom("ses-test", 2); err != nil {
		t.Fatalf("DeleteMessagesFrom: %v", err)
	}

	got, err := db.GetMessages("ses-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ses-test rows = %d; want 2 (idx 0,1)", len(got))
	}
	for i, m := range got {
		if m.Idx != i {
			t.Errorf("row %d has idx %d", i, m.Idx)
		}
	}

	other, err := db.GetMessages("other")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 {
		t.Errorf("other session rows = %d; want 1", len(other))
	}
}

func idAt(i int) string {
	return string(rune('a'+i)) + "-msg"
}

// TestDB_AddMessage_EmbedFailureSurfaced pins GitHub #311 for the
// message path: a failing embedder must log a rate-limited warning
// naming the operation and the error, and the message row must persist
// with a NULL embedding instead of failing the write.
func TestDB_AddMessage_EmbedFailureSurfaced(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)
	db.SetEmbedder(failingEmbedder{errors.New("no route to host")})

	if err := db.AddMessage(msg("fm-1", "user", 0)); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// embedMessageAsync runs in a goroutine and AddMessage does not
	// expose its done channel — poll the captured log instead.
	deadline := time.Now().Add(2 * time.Second)
	var out string
	for time.Now().Before(deadline) {
		out = logs.String()
		if strings.Contains(out, "embed_message") {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Final snapshot: the async warn may have landed during the last
	// sleep, after the loop's final read.
	out = logs.String()
	if !strings.Contains(out, "embedding failed") {
		t.Fatalf("expected warning log, got: %q", out)
	}
	if !strings.Contains(out, "no route to host") {
		t.Errorf("expected error in log, got: %q", out)
	}

	var blob []byte
	if err := db.sql.QueryRow(`SELECT COALESCE(embedding, '') FROM messages WHERE id = 'fm-1'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if len(blob) != 0 {
		t.Errorf("failed embed must leave embedding NULL, got %d bytes", len(blob))
	}
}

func TestDB_SearchMessagesVector_EmbedFailureDegradesToNil(t *testing.T) {
	logs := captureWarnLogs(t)
	db := openTestDB(t)
	db.SetEmbedder(failingEmbedder{errors.New("connection reset")})

	results, err := db.SearchMessagesVector(t.Context(), "anything", 5)
	if err != nil {
		t.Fatalf("SearchMessagesVector must degrade, got error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results on embed failure, got %d", len(results))
	}

	out := logs.String()
	if !strings.Contains(out, "search_messages_vector") {
		t.Errorf("expected op name in log, got: %q", out)
	}
	if !strings.Contains(out, "connection reset") {
		t.Errorf("expected error in log, got: %q", out)
	}
}
