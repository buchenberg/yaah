package memory

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrate_AddsCrossLinkColumns verifies that an existing database
// created before the consolidate-persistence Phase 0 migration gains the
// messages.trace_id / messages.turn_id columns and the trace_owners table
// on Open.
func TestMigrate_AddsCrossLinkColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	// Create a legacy database with the pre-Phase-0 messages schema.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	legacySchema := `
	CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		started_at INTEGER NOT NULL
	);
	CREATE TABLE messages (
		session_id TEXT NOT NULL REFERENCES sessions(id),
		idx INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		ts INTEGER NOT NULL,
		PRIMARY KEY (session_id, idx)
	);
	INSERT INTO sessions (id, started_at) VALUES ('s1', 1);
	INSERT INTO messages (session_id, idx, role, content, ts) VALUES ('s1', 0, 'user', 'hi', 1);
	`
	if _, err := legacy.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	legacy.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer d.Close()

	for _, col := range []string{"trace_id", "turn_id"} {
		var n int
		if err := d.sql.QueryRow("SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = ?", col).Scan(&n); err != nil {
			t.Fatalf("pragma %s: %v", col, err)
		}
		if n != 1 {
			t.Errorf("messages.%s column missing after migration", col)
		}
	}

	var n int
	if err := d.sql.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='trace_owners'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("trace_owners table missing after migration")
	}

	// The legacy row must survive untouched.
	var content string
	if err := d.sql.QueryRow(`SELECT content FROM messages WHERE session_id = 's1' AND idx = 0`).Scan(&content); err != nil {
		t.Fatalf("legacy row lost: %v", err)
	}
	if content != "hi" {
		t.Errorf("legacy row content = %q, want %q", content, "hi")
	}
}

// TestRecordTraceOwner verifies the subTraceID -> parentSession mapping
// roundtrip that makes Shepherd facts joinable to parent sessions.
func TestRecordTraceOwner(t *testing.T) {
	d := openTestDB(t)
	defer d.Close()

	const owner = "sub-analyst-sess1-1700000000"
	if err := d.RecordTraceOwner(owner, "sess1", "analyst"); err != nil {
		t.Fatalf("RecordTraceOwner: %v", err)
	}

	parent, err := d.TraceOwnerParent(owner)
	if err != nil {
		t.Fatalf("TraceOwnerParent: %v", err)
	}
	if parent != "sess1" {
		t.Errorf("parent = %q, want %q", parent, "sess1")
	}

	// Re-recording the same owner is idempotent.
	if err := d.RecordTraceOwner(owner, "sess-other", "planner"); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	parent, _ = d.TraceOwnerParent(owner)
	if parent != "sess1" {
		t.Errorf("parent after idempotent re-record = %q, want original %q", parent, "sess1")
	}

	if _, err := d.TraceOwnerParent("unknown-owner"); err != sql.ErrNoRows {
		t.Errorf("unknown owner error = %v, want sql.ErrNoRows", err)
	}
}

// TestAddMessage_PersistsTurnContext verifies that trace/turn cross-link
// IDs survive the insert roundtrip.
func TestAddMessage_PersistsTurnContext(t *testing.T) {
	d := openTestDB(t)
	defer d.Close()

	// messages.session_id references sessions(id) with FK enforcement on.
	if _, err := d.sql.Exec(`INSERT INTO sessions (id, started_at) VALUES ('s1', 1)`); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if err := d.AddMessage(Message{
		ID: "m1", SessionID: "s1", Idx: 0, Role: "assistant",
		Content: "answer", Timestamp: 1,
		TraceID: "abc123", TurnID: "def456",
	}); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	var traceID, turnID string
	if err := d.sql.QueryRow(`SELECT trace_id, turn_id FROM messages WHERE session_id = 's1' AND idx = 0`).Scan(&traceID, &turnID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if traceID != "abc123" || turnID != "def456" {
		t.Errorf("trace_id = %q turn_id = %q, want abc123/def456", traceID, turnID)
	}
}
