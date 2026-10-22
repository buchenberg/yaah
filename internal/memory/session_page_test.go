package memory

import (
	"errors"
	"strings"
	"testing"
)

// TestDB_TouchSessionTitleAndActivity pins the picker bookkeeping hooked
// into AddMessage: the first real user message adopts the title, later
// user messages do not change it, and every persisted message moves
// updated_at to its timestamp.
func TestDB_TouchSessionTitleAndActivity(t *testing.T) {
	db := openTestDB(t)

	if err := db.AddMessage(Message{ID: "m-0", SessionID: "ses-test", Idx: 0, Role: "user", Content: "Fix the parser bug:\n  it drops tokens", Timestamp: 100}); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	got, err := db.GetSession("ses-test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Fix the parser bug: it drops tokens" {
		t.Fatalf("title = %q", got.Title)
	}
	if got.UpdatedAt != 100 {
		t.Fatalf("updated_at = %d, want 100", got.UpdatedAt)
	}

	// A later user message refreshes activity but not the title.
	if err := db.AddMessage(Message{ID: "m-1", SessionID: "ses-test", Idx: 1, Role: "user", Content: "New question", Timestamp: 200}); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	got, err = db.GetSession("ses-test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Fix the parser bug: it drops tokens" {
		t.Fatalf("title changed after second message: %q", got.Title)
	}
	if got.UpdatedAt != 200 {
		t.Fatalf("updated_at = %d, want 200", got.UpdatedAt)
	}
}

// TestDB_SyntheticUserContentNeverTitles pins the title-skip rules for
// persisted user-role rows that carry agent-internal traffic (steering,
// supervision, conflicts, background dispatch).
func TestDB_SyntheticUserContentNeverTitles(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "steering", content: "[STEER] Keep it short"},
		{name: "supervision", content: "SUPERVISOR: continue"},
		{name: "conflict", content: "CONFLICT: watchers clash"},
		{name: "background", content: "[BACKGROUND explore] result"},
		{name: "generic bracket", content: "[checkpoint recovery] advice"},
		{name: "whitespace only", content: "   \n\t "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			if err := db.AddMessage(Message{ID: "m-0", SessionID: "ses-test", Idx: 0, Role: "user", Content: tc.content, Timestamp: 5}); err != nil {
				t.Fatalf("AddMessage: %v", err)
			}
			got, err := db.GetSession("ses-test")
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != "" {
				t.Fatalf("title = %q, want empty", got.Title)
			}
			if got.UpdatedAt != 5 {
				t.Fatalf("updated_at = %d, want 5 (synthetic rows still count as activity)", got.UpdatedAt)
			}
		})
	}
}

// TestDB_DeriveSessionTitle pins the condensation rules.
func TestDB_DeriveSessionTitle(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "collapse whitespace", content: "  fix\tthe\n   parser  ", want: "fix the parser"},
		{name: "truncated with marker", content: strings.Repeat("a", 90), want: strings.Repeat("a", 80) + "…"},
		{name: "exactly 80 runes kept", content: strings.Repeat("b", 80), want: strings.Repeat("b", 80)},
		{name: "internal marker skipped", content: "SUPERVISOR: retry", want: ""},
		{name: "empty", content: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveSessionTitle(tc.content); got != tc.want {
				t.Fatalf("deriveSessionTitle(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

// TestDB_ListSessionsPage pins the paged listing used by ACP
// session/list and the yaah sessions CLI: stable newest-first keyset
// ordering, cwd filtering, per-row counts, the activity fallback for
// message-less sessions, cursor chaining, and invalid-cursor errors.
func TestDB_ListSessionsPage(t *testing.T) {
	db := openTestDB(t)

	// ses-test exists (openTestDB); four controlled sessions, three in
	// the "c1" workspace, one elsewhere. deliberately unordered inserts.
	fixtures := []struct {
		id string
		c  string
		s  int64
	}{
		{"s1", "c1", 30},
		{"s2", "c1", 20},
		{"s3", "c2", 40},
		{"s4", "c1", 10},
	}
	for _, f := range fixtures {
		if err := db.CreateSession(Session{ID: f.id, CWD: f.c, StartedAt: f.s}); err != nil {
			t.Fatalf("CreateSession %s: %v", f.id, err)
		}
	}
	// s1 gets two messages (so updated_at moves), s2 gets one.
	for _, m := range []Message{
		{ID: "s1-0", SessionID: "s1", Idx: 0, Role: "user", Content: "hi", Timestamp: 31},
		{ID: "s1-1", SessionID: "s1", Idx: 1, Role: "assistant", Content: "hey", Timestamp: 32},
		{ID: "s2-0", SessionID: "s2", Idx: 0, Role: "user", Content: "ho", Timestamp: 25},
	} {
		if err := db.AddMessage(m); err != nil {
			t.Fatalf("AddMessage %s: %v", m.ID, err)
		}
	}

	// Page one: c1 newest first, 2 rows, lookahead produces a cursor.
	rows, next, err := db.ListSessionsPage(SessionListFilter{CWD: "c1", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "s1" || rows[1].ID != "s2" {
		t.Fatalf("page one = %+v", rows)
	}
	if rows[0].UpdatedAt != 32 || rows[1].UpdatedAt != 25 {
		t.Fatalf("updated_at propagation broken: %+v", rows)
	}
	if rows[0].MessageCount != 2 || rows[1].MessageCount != 1 {
		t.Fatalf("message counts broken: %+v", rows)
	}
	if next == "" {
		t.Fatal("expected a next cursor with rows remaining")
	}

	// Page two: the last c1 row follows the cursor, no further cursor.
	rows, next2, err := db.ListSessionsPage(SessionListFilter{CWD: "c1", Cursor: next, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "s4" {
		t.Fatalf("page two = %+v", rows)
	}
	if rows[0].UpdatedAt != 10 {
		t.Fatalf("message-less session must fall back to started_at: %+v", rows[0])
	}
	if next2 != "" {
		t.Fatalf("final page must not advertise a next cursor (%q)", next2)
	}

	// Unmatched filter: empty slice, no error.
	rows, next, err = db.ListSessionsPage(SessionListFilter{CWD: "elsewhere", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || next != "" {
		t.Fatalf("unmatched filter = (%+v, %q)", rows, next)
	}

	// Invalid cursor: the shared sentinel.
	if _, _, err := db.ListSessionsPage(SessionListFilter{Cursor: "@@@@@"}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("invalid cursor error = %v, want ErrInvalidCursor", err)
	}
}

// TestDB_DeleteSession pins the delete semantics: children go first, the
// second delete reports ErrSessionNotFound, other sessions and their
// messages survive, and stale trace_owners rows pointing at the deleted
// session are pruned.
func TestDB_DeleteSession(t *testing.T) {
	db := openTestDB(t)

	if err := db.AddMessage(Message{ID: "m-0", SessionID: "ses-test", Idx: 0, Role: "user", Content: "hi", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO trace_owners (owner_id, parent_session, role, created_at) VALUES ('own-1', 'ses-test', 'sub', 2)`); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteSession("ses-test"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := db.GetSession("ses-test"); err == nil {
		t.Fatal("deleted session still readable")
	}
	msgs, err := db.GetMessages("ses-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("messages survived: %d row(s)", len(msgs))
	}
	var owners int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM trace_owners WHERE parent_session = 'ses-test'`).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if owners != 0 {
		t.Fatalf("trace_owners rows survived: %d", owners)
	}

	if err := db.DeleteSession("ses-test"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second delete = %v, want ErrSessionNotFound", err)
	}

	// Other sessions and their messages stay intact.
	if err := db.CreateSession(Session{ID: "survivor", StartedAt: 50}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMessage(Message{ID: "sv-0", SessionID: "survivor", Idx: 0, Role: "user", Content: "still here", Timestamp: 51}); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetMessages("survivor"); err != nil || len(got) != 1 {
		t.Fatalf("survivor rows = %d (%v), want 1", len(got), err)
	}
}

// TestDB_BackfillLegacyActivity pins the N-1 migration step: a session
// row written before updated_at existed is backfilled from started_at by
// the next migrate, so old sessions list with usable activity times.
func TestDB_BackfillLegacyActivity(t *testing.T) {
	db := openTestDB(t)
	const end = 1234567890
	if err := db.CreateSession(Session{ID: "legacy", StartedAt: end}); err != nil {
		t.Fatal(err)
	}
	// A fresh CreateSession seeds activity from started_at…
	if got, err := db.GetSession("legacy"); err != nil || got.UpdatedAt != end {
		t.Fatalf("created row updated_at = %d (%v), want started_at %d", got.UpdatedAt, err, end)
	}
	// …but a row written by an older binary has 0, and the migrate
	// backfill (run on every open) repairs it.
	if _, err := db.sql.Exec(`UPDATE sessions SET updated_at = 0 WHERE id = 'legacy'`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if got, err := db.GetSession("legacy"); err != nil || got.UpdatedAt != end {
		t.Fatalf("backfilled updated_at = %d (%v), want started_at %d", got.UpdatedAt, err, end)
	}
}
