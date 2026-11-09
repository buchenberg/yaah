package memory

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionPersona_CarriesAtCreation pins the write half of the
// persona persistence (yaah-21uk.9): a session created with a resolved
// persona name stores it, and both the dedicated reader and the full row
// read it back.
func TestSessionPersona_CarriesAtCreation(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateSession(Session{ID: "ses-persona", StartedAt: time.Now().Unix(), Persona: "planner"}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	got, err := db.SessionPersona("ses-persona")
	if err != nil {
		t.Fatalf("SessionPersona() error: %v", err)
	}
	if got != "planner" {
		t.Fatalf("SessionPersona() = %q; want %q", got, "planner")
	}
	row, err := db.GetSession("ses-persona")
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if row.Persona != "planner" {
		t.Errorf("GetSession().Persona = %q; want %q", row.Persona, "planner")
	}
}

// The persona column defaults to the empty string, which the session
// layer reads as build: a session created without a persona must not
// claim one.
func TestSessionPersona_DefaultsEmpty(t *testing.T) {
	db := openTestDB(t)

	got, err := db.SessionPersona("ses-test")
	if err != nil {
		t.Fatalf("SessionPersona() error: %v", err)
	}
	if got != "" {
		t.Fatalf("SessionPersona() = %q on a session created without a persona; want \"\" (build)", got)
	}
}

// The read has to survive the handle: resume resolves the persona from a
// fresh connection, so a name written at creation must still be there.
func TestSessionPersona_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if err := db.CreateSession(Session{ID: "ses-persona", StartedAt: time.Now().Unix(), Persona: "planner"}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	got, err := reopened.SessionPersona("ses-persona")
	if err != nil {
		t.Fatalf("SessionPersona() after reopen error: %v", err)
	}
	if got != "planner" {
		t.Fatalf("SessionPersona() = %q after reopen; want %q", got, "planner")
	}
}

// An unknown id must be an error, not a silent build: the resume path has
// to be able to tell "no persona stored" from "no such session" (the
// restore fails on the missing session with its own, precise error).
func TestSessionPersona_UnknownSession(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.SessionPersona("ses-ghost"); err == nil {
		t.Fatal("SessionPersona() on an unknown id = nil error; want an error")
	}
}

// TestSessionPersona_LegacyRowsReadAsBuild pins the zero-change
// migration: a database forged in the pre-persona shape migrates on open
// (the column is added), and its rows read as "" — build at the
// resolution sites — rather than sprouting a persona they never had.
func TestSessionPersona_LegacyRowsReadAsBuild(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE schema_meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		INSERT INTO schema_meta (key, value) VALUES ('version', '1');
		CREATE TABLE sessions (
			id         TEXT PRIMARY KEY,
			started_at INTEGER NOT NULL,
			ended_at   INTEGER,
			cwd        TEXT,
			model      TEXT,
			tokens_in  INTEGER DEFAULT 0,
			tokens_out INTEGER DEFAULT 0
		);
		CREATE TABLE messages (
			session_id        TEXT NOT NULL REFERENCES sessions(id),
			idx               INTEGER NOT NULL,
			role              TEXT NOT NULL,
			content           TEXT NOT NULL,
			reasoning_content TEXT DEFAULT '',
			reasoning_signature TEXT DEFAULT '',
			tool_name         TEXT,
			tool_call_id      TEXT,
			tool_calls        TEXT,
			ts                INTEGER NOT NULL,
			PRIMARY KEY (session_id, idx)
		);
		INSERT INTO sessions (id, started_at, cwd) VALUES ('legacy', 1234567890, '/x');
		INSERT INTO messages (session_id, idx, role, content, ts) VALUES ('legacy', 0, 'user', 'hi', 1234567891);
	`)
	if err != nil {
		t.Fatalf("forge legacy db: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(legacyPath)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	got, err := db.SessionPersona("legacy")
	if err != nil {
		t.Fatalf("SessionPersona() on a migrated legacy row error: %v", err)
	}
	if got != "" {
		t.Fatalf("SessionPersona() = %q on a migrated legacy row; want \"\" (build)", got)
	}
}
