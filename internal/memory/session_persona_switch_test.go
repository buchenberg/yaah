package memory

import (
	"path/filepath"
	"testing"
	"time"
)

// openPersonaSwitchStore creates a store with one session row, the shape
// a live session switch persists against.
func openPersonaSwitchStore(t *testing.T, id string) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "persona-switch.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSession(Session{ID: id, StartedAt: time.Now().Unix(), Persona: "planner"}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	return db
}

// SetSessionPersona rewrites the column a resume re-resolves, so a
// switch outlives the process. The empty string is build's storage form.
func TestSetSessionPersonaRewritesStoredName(t *testing.T) {
	db := openPersonaSwitchStore(t, "ses-pswitch")

	if got, err := db.SessionPersona("ses-pswitch"); err != nil || got != "planner" {
		t.Fatalf("SessionPersona() = %q, %v; want the created %q", got, err, "planner")
	}
	if err := db.SetSessionPersona("ses-pswitch", "builder"); err != nil {
		t.Fatalf("SetSessionPersona() error: %v", err)
	}
	if got, err := db.SessionPersona("ses-pswitch"); err != nil || got != "builder" {
		t.Fatalf("SessionPersona() after switch = %q, %v; want %q", got, err, "builder")
	}
	// Back to build: the empty name is the stored form.
	if err := db.SetSessionPersona("ses-pswitch", ""); err != nil {
		t.Fatalf("SetSessionPersona(\"\") error: %v", err)
	}
	if got, err := db.SessionPersona("ses-pswitch"); err != nil || got != "" {
		t.Fatalf("SessionPersona() after switching back = %q, %v; want \"\" (build)", got, err)
	}
}

// The switch is written on the switch, not at close, so a killed process
// still leaves the selection on the row — reopen must see it.
func TestSetSessionPersonaPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persona-reopen.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if err := db.CreateSession(Session{ID: "ses-reopen", StartedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := db.SetSessionPersona("ses-reopen", "builder"); err != nil {
		t.Fatalf("SetSessionPersona() error: %v", err)
	}
	db.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen Open() error: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	if got, err := reopened.SessionPersona("ses-reopen"); err != nil || got != "builder" {
		t.Fatalf("SessionPersona() after reopen = %q, %v; want %q", got, err, "builder")
	}
}

// A persona switch rebinds the persona body into the prompt, and resume
// reads system_prompt byte-for-byte — so the switch must be able to
// replace the stored prompt.
func TestSetSessionSystemPromptReplacesStoredPrompt(t *testing.T) {
	db := openPersonaSwitchStore(t, "ses-sysprompt")
	if err := db.SetSessionSystemPrompt("ses-sysprompt", "REBUILT PROMPT BYTES"); err != nil {
		t.Fatalf("SetSessionSystemPrompt() error: %v", err)
	}
	row, err := db.GetSession("ses-sysprompt")
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if row.SystemPrompt != "REBUILT PROMPT BYTES" {
		t.Fatalf("stored SystemPrompt = %q; want the rebuilt bytes", row.SystemPrompt)
	}
}
