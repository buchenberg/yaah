package agent

import (
	"path/filepath"
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/types"
)

// TestSessionPersister_titleTransition pins the client-facing title
// contract: the hook fires exactly when the persisted title transitions
// from empty — the first user message that derives a title — never for
// synthetic traffic or later user messages, and the value equals what
// TouchSession sets in the store.
func TestSessionPersister_titleTransition(t *testing.T) {
	dir := t.TempDir()
	db, err := memory.Open(filepath.Join(dir, "persist.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSession(memory.Session{ID: "sess-t", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}

	var fired []string
	p := NewSessionPersister(db, nil, "sess-t")
	p.KnownTitle = ""
	p.OnTitle = func(title string, updatedAt int64) {
		fired = append(fired, title)
	}

	// Synthetic first traffic must not title the session (the store's
	// touch derives "" for these and leaves the column empty).
	p.Persist(types.UserMsg("[STEER] keep tests stable"))
	p.Persist(types.UserMsg("SUPERVISOR: reconcile after the run"))
	if len(fired) != 0 {
		t.Fatalf("hook fired for synthetic messages: %v", fired)
	}

	// The first real user message transitions the title.
	p.Persist(types.UserMsg("fix the flaky test"))
	if len(fired) != 1 || fired[0] != "fix the flaky test" {
		t.Fatalf("hook = %v, want exactly one \"fix the flaky test\"", fired)
	}

	// KnownTitle now blocks further transitions; a second user message
	// (and a tool result) must stay silent.
	p.Persist(types.UserMsg("another turn here"))
	p.Persist(types.Message{Role: "tool", Name: "read", Content: "some file"})
	if len(fired) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(fired))
	}

	// The store derives the identical value from the same content.
	if row, err := db.GetSession("sess-t"); err != nil || row.Title != "fix the flaky test" {
		t.Fatalf("store title = %q (%v), want derived value", row.Title, err)
	}
}

// TestSessionPersister_knownTitleSeeded pins the resumed-session rule: a
// persister seeded from the store (KnownTitle preset) never re-fires the
// transition, mirroring TouchSession's store-side smoke.
func TestSessionPersister_knownTitleSeeded(t *testing.T) {
	dir := t.TempDir()
	db, err := memory.Open(filepath.Join(dir, "persist.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSession(memory.Session{ID: "sess-r", StartedAt: 1, Title: "earlier"}); err != nil {
		t.Fatal(err)
	}

	var fired int
	p := NewSessionPersister(db, nil, "sess-r")
	p.KnownTitle = "earlier"
	p.OnTitle = func(string, int64) { fired++ }
	p.Persist(types.UserMsg("resumed conversation"))
	if fired != 0 {
		t.Fatalf("hook fired %d times for a titled, resumed session", fired)
	}
}
