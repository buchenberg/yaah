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

// TestSearchMessagesVector_EmptyVecIndexFallsBackToScan pins the window fix
// for the CI flake in TestMemorySessionSearchTool_SemanticContextAndHybridUnion:
// a search that lands after the embed goroutine wrote the BLOB but before (or
// without) its vec0 mirror must still find the row. An empty index with a nil
// error used to return zero results, so the caller labeled a working store as
// "semantic search returned no candidates". The BLOB column is the source of
// truth; the scan is correct by construction.
func TestSearchMessagesVector_EmptyVecIndexFallsBackToScan(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(stubEmbedder{})
	addMessageAndWait(t, db, Message{SessionID: "ses-test", Idx: 0, Role: "user", Content: "postgres connection pooling", Timestamp: 1, ID: "m1"})

	if !db.messagesVecPresent() {
		t.Fatal("precondition: the messages vec0 index must exist after a mirrored embed")
	}
	if _, err := db.sql.Exec(`DELETE FROM messages_vec`); err != nil {
		t.Fatalf("clear vec0 rows: %v", err)
	}

	results, err := db.SearchMessagesVector(t.Context(), "postgres pooling", 10)
	if err != nil {
		t.Fatalf("SearchMessagesVector: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("empty vec0 index must degrade to the scan, not report no candidates")
	}
	if results[0].ID != "m1" {
		t.Errorf("scan returned %q, want m1", results[0].ID)
	}
}

// TestSearchMessagesVector_ScanSkipsStaleWidthRows pins the scan-side width
// guard: rows embedded by a previous model are not cosine-scored against the
// current query vector (meaningless, and a 0 score used to leak them into the
// page); they are skipped and surfaced via the rate-limited dim-mismatch
// warning instead.
func TestSearchMessagesVector_ScanSkipsStaleWidthRows(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(dimEmbedder{dims: 2, base: 0.5})
	addMessageAndWait(t, db, Message{SessionID: "ses-test", Idx: 0, Role: "user", Content: "postgres pooling", Timestamp: 1, ID: "m1"})

	db.SetEmbedder(dimEmbedder{dims: 4, base: 0.3})

	logs := captureWarnLogs(t)
	results, err := db.SearchMessagesVector(t.Context(), "postgres pooling", 10)
	if err != nil {
		t.Fatalf("SearchMessagesVector: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("stale-width rows must not be scored against a wider query, got %d", len(results))
	}
	if !strings.Contains(logs.String(), "stored embedding dimensions differ") {
		t.Errorf("expected the dim-mismatch warning, got: %q", logs.String())
	}
}
