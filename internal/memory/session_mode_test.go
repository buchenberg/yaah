package memory

import (
	"path/filepath"
	"testing"
	"time"
)

// The mode column defaults to the empty string, which the session layer reads
// as build: a row created before the column existed (or by a caller that never
// switched) must not claim a mode.
func TestSessionMode_DefaultsToBuild(t *testing.T) {
	db := openTestDB(t) // creates ses-test

	mode, err := db.SessionMode("ses-test")
	if err != nil {
		t.Fatalf("SessionMode() error: %v", err)
	}
	if mode != "" {
		t.Fatalf("SessionMode() = %q on a fresh session; want \"\" (build)", mode)
	}
}

// SetSessionMode/SessionMode round-trip every mode the session layer can hold,
// including the empty build value a caller may write back.
func TestSessionMode_RoundTrip(t *testing.T) {
	db := openTestDB(t)

	for _, want := range []string{"plan", "build", ""} {
		t.Run("mode="+want, func(t *testing.T) {
			if err := db.SetSessionMode("ses-test", want); err != nil {
				t.Fatalf("SetSessionMode(%q) error: %v", want, err)
			}
			got, err := db.SessionMode("ses-test")
			if err != nil {
				t.Fatalf("SessionMode() error: %v", err)
			}
			if got != want {
				t.Fatalf("SessionMode() = %q after SetSessionMode(%q); want %q", got, want, want)
			}
		})
	}
}

// The switch has to survive the process, not just the handle: resume reads the
// row from a fresh connection, so a mode written here must still be there.
func TestSessionMode_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if err := db.CreateSession(Session{ID: "ses-mode", StartedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := db.SetSessionMode("ses-mode", "plan"); err != nil {
		t.Fatalf("SetSessionMode() error: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	got, err := reopened.SessionMode("ses-mode")
	if err != nil {
		t.Fatalf("SessionMode() after reopen error: %v", err)
	}
	if got != "plan" {
		t.Fatalf("SessionMode() = %q after reopen; want %q", got, "plan")
	}
}

// An unknown id must be an error, not a silent build: restore has to be able to
// tell "no such session" from "no mode stored".
func TestSessionMode_UnknownSession(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.SessionMode("ses-ghost"); err == nil {
		t.Fatal("SessionMode() on an unknown id = nil error; want an error")
	}
}
