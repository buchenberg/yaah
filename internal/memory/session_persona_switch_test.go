package memory

import (
	"path/filepath"
	"strings"
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

// TestSetSessionPersonaState pins the atomic three-column write a live
// persona switch uses (yaah-21uk.17): the persona, the rebuilt system prompt
// and the resolved routing pick land together, and an empty prompt or
// incomplete pick leaves those columns exactly as they were so the call
// cannot erase state the switch did not change.
func TestSetSessionPersonaState(t *testing.T) {
	t.Run("records persona, prompt and pick", func(t *testing.T) {
		db := openPersonaSwitchStore(t, "ses-state-all")
		if err := db.SetSessionSystemPrompt("ses-state-all", "OLD PROMPT"); err != nil {
			t.Fatalf("SetSessionSystemPrompt() error: %v", err)
		}
		if err := db.SetSessionModel("ses-state-all", "oldprov", "oldmodel"); err != nil {
			t.Fatalf("SetSessionModel() error: %v", err)
		}
		// The model half carries slashes of its own: the column joins the two
		// halves with "/" and the resume path splits on the FIRST one, so a
		// slash-bearing model must survive the round trip.
		if err := db.SetSessionPersonaState("ses-state-all", "plan", "REBUILT PROMPT", "prov", "gpt/x"); err != nil {
			t.Fatalf("SetSessionPersonaState() error: %v", err)
		}
		row, err := db.GetSession("ses-state-all")
		if err != nil {
			t.Fatalf("GetSession() error: %v", err)
		}
		if row.Persona != "plan" {
			t.Errorf("Persona = %q; want plan", row.Persona)
		}
		if row.SystemPrompt != "REBUILT PROMPT" {
			t.Errorf("SystemPrompt = %q; want the rebuilt bytes", row.SystemPrompt)
		}
		if row.Model != "prov/gpt/x" {
			t.Errorf("Model = %q; want prov/gpt/x", row.Model)
		}
	})

	t.Run("empty prompt and pick leave their columns untouched", func(t *testing.T) {
		db := openPersonaSwitchStore(t, "ses-state-skip")
		if err := db.SetSessionSystemPrompt("ses-state-skip", "KEPT PROMPT"); err != nil {
			t.Fatalf("SetSessionSystemPrompt() error: %v", err)
		}
		if err := db.SetSessionModel("ses-state-skip", "keptprov", "keptmodel"); err != nil {
			t.Fatalf("SetSessionModel() error: %v", err)
		}
		if err := db.SetSessionPersonaState("ses-state-skip", "plan", "", "", ""); err != nil {
			t.Fatalf("SetSessionPersonaState() error: %v", err)
		}
		row, err := db.GetSession("ses-state-skip")
		if err != nil {
			t.Fatalf("GetSession() error: %v", err)
		}
		if row.Persona != "plan" {
			t.Errorf("Persona = %q; want the switch recorded", row.Persona)
		}
		if row.SystemPrompt != "KEPT PROMPT" {
			t.Errorf("SystemPrompt = %q; an empty prompt must not clear the column", row.SystemPrompt)
		}
		if row.Model != "keptprov/keptmodel" {
			t.Errorf("Model = %q; an empty pick must not clear the column", row.Model)
		}
	})

	t.Run("a half-set pick does not overwrite the model", func(t *testing.T) {
		db := openPersonaSwitchStore(t, "ses-state-half")
		if err := db.SetSessionModel("ses-state-half", "keptprov", "keptmodel"); err != nil {
			t.Fatalf("SetSessionModel() error: %v", err)
		}
		if err := db.SetSessionPersonaState("ses-state-half", "plan", "", "newprov", ""); err != nil {
			t.Fatalf("SetSessionPersonaState() error: %v", err)
		}
		row, err := db.GetSession("ses-state-half")
		if err != nil {
			t.Fatalf("GetSession() error: %v", err)
		}
		if row.Model != "keptprov/keptmodel" {
			t.Errorf("Model = %q; a provider without a model is not routable", row.Model)
		}
	})

	t.Run("unknown session names the id and writes nothing", func(t *testing.T) {
		db := openPersonaSwitchStore(t, "ses-state-real")
		err := db.SetSessionPersonaState("ses-state-missing", "plan", "PROMPT", "prov", "m")
		if err == nil {
			t.Fatal("SetSessionPersonaState() on a missing session must fail")
		}
		if !strings.Contains(err.Error(), "no session") || !strings.Contains(err.Error(), "ses-state-missing") {
			t.Errorf("error = %v; want it to name the missing session", err)
		}
		row, err := db.GetSession("ses-state-real")
		if err != nil {
			t.Fatalf("GetSession() error: %v", err)
		}
		if row.Persona != "planner" || row.SystemPrompt != "" || row.Model != "" {
			t.Errorf("a refused write must leave the row alone: persona=%q prompt=%q model=%q",
				row.Persona, row.SystemPrompt, row.Model)
		}
	})
}
