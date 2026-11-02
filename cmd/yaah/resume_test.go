package yaah

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
)

// TestRestoreMessage_carriesReasoningSignature pins that resume does not drop
// the thinking signature. A thinking block is only re-emitted when both its
// content and its signature are present, so losing the signature here makes
// replay silently stop working after `yaah resume` even though the column
// was loaded from the DB.
func TestRestoreMessage_carriesReasoningSignature(t *testing.T) {
	got := restoreMessage(memory.Message{
		Role:               "assistant",
		Content:            "answer",
		ReasoningContent:   "because",
		ReasoningSignature: "sig-1",
		ToolName:           "bash",
		ToolCallID:         "call-1",
	})

	if got.ReasoningContent != "because" {
		t.Errorf("ReasoningContent = %q", got.ReasoningContent)
	}
	if got.ReasoningSignature != "sig-1" {
		t.Errorf("ReasoningSignature = %q, want %q", got.ReasoningSignature, "sig-1")
	}
	if got.Name != "bash" || got.ToolCallID != "call-1" {
		t.Errorf("tool identity lost: name=%q id=%q", got.Name, got.ToolCallID)
	}
}

// newResumeFixture persists a session with one message, so restore's
// "no messages" guard does not fire.
func newResumeFixture(t *testing.T, id string) *memory.DB {
	t.Helper()
	db, err := memory.Open(filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatalf("memory.Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().Unix()
	if err := db.CreateSession(memory.Session{ID: id, StartedAt: now}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := db.AddMessage(memory.Message{
		ID: "msg-1", SessionID: id, Idx: 0, Role: "user", Content: "plan this work", Timestamp: now,
	}); err != nil {
		t.Fatalf("AddMessage() error: %v", err)
	}
	return db
}

// A resumed session comes back in the mode it was left in: the mode column is
// read on restore, so a plan-mode session is not silently widened to build.
func TestRestoreSession_RecoversPersistedMode(t *testing.T) {
	const id = "sess-plan"
	db := newResumeFixture(t, id)
	if err := db.SetSessionMode(id, acpModePlan); err != nil {
		t.Fatalf("SetSessionMode() error: %v", err)
	}

	got, err := restoreSession(db, id, "BASE PROMPT")
	if err != nil {
		t.Fatalf("restoreSession() error: %v", err)
	}
	if got.Mode != acpModePlan {
		t.Fatalf("restored Mode = %q; want %q", got.Mode, acpModePlan)
	}
	if got.SessionID != id {
		t.Fatalf("restored SessionID = %q; want %q", got.SessionID, id)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("restored %d message(s); want 1", len(got.Messages))
	}
}

// A session that was never switched restores as build (the empty mode).
func TestRestoreSession_FreshSessionIsBuild(t *testing.T) {
	got, err := restoreSession(nil, "", "BASE PROMPT")
	if err != nil {
		t.Fatalf("restoreSession() error: %v", err)
	}
	if got.Mode != "" {
		t.Fatalf("Mode = %q for a fresh session; want \"\" (build)", got.Mode)
	}
	if got.SystemPrompt != "BASE PROMPT" {
		t.Fatalf("SystemPrompt = %q; want the caller's prompt", got.SystemPrompt)
	}
}

// A persisted mode this binary cannot enforce must not be restored: a row
// written by a newer yaah (or edited by hand) falls back to build rather than
// putting the session in a regime the loop does not implement.
func TestRestoreSession_IgnoresUnenforceableMode(t *testing.T) {
	const id = "sess-odd"
	db := newResumeFixture(t, id)
	if err := db.SetSessionMode(id, "yolo"); err != nil {
		t.Fatalf("SetSessionMode() error: %v", err)
	}

	got, err := restoreSession(db, id, "BASE PROMPT")
	if err != nil {
		t.Fatalf("restoreSession() error: %v", err)
	}
	if got.Mode != "" {
		t.Fatalf("restored Mode = %q for an unenforceable persisted mode; want \"\" (build)", got.Mode)
	}
}

// A restore is not a switch: restoreSession recovers the persisted mode into
// its result and nothing else. Exercised through the real restore path, because
// a session literal built with mode: plan proves nothing about restore — no
// code in that setup could emit in the first place.
func TestRestoredModeIsNotAnnounced(t *testing.T) {
	const id = "sess-restored-plan"
	db := newResumeFixture(t, id)
	if err := db.SetSessionMode(id, acpModePlan); err != nil {
		t.Fatalf("SetSessionMode() error: %v", err)
	}

	got, err := restoreSession(db, id, "BASE PROMPT")
	if err != nil {
		t.Fatalf("restoreSession() error: %v", err)
	}
	if got.Mode != acpModePlan {
		t.Fatalf("restored Mode = %q; want %q", got.Mode, acpModePlan)
	}

	// A session built from that result starts in plan mode without a switch,
	// so the view carrying it hears nothing. The positive control below (a real
	// SetMode does emit) is what gives the zero-event assertion its meaning.
	sess := &agentSession{mode: got.Mode}
	view := &safeCaptureView{}
	sess.SetView(view)
	if len(view.events) != 0 {
		t.Fatalf("view received %d event(s) after restoring mode %q; want 0", len(view.events), got.Mode)
	}
	if sess.Mode() != acpModePlan {
		t.Fatalf("Mode() = %q; want %q", sess.Mode(), acpModePlan)
	}
	if err := sess.SetMode(acpModeBuild); err != nil {
		t.Fatalf("SetMode(%q) = %v", acpModeBuild, err)
	}
	if len(view.events) != 1 {
		t.Fatalf("view received %d event(s) after a real switch; want 1 (the observer must work for the restore assertion to mean anything)", len(view.events))
	}
}

// modeReadFailStore makes only the mode read fail: the embedded store serves
// the session row and messages normally, so restoreSession reaches
// persistedMode (the fail-closed path) rather than failing earlier.
type modeReadFailStore struct {
	*memory.DB
}

func (f *modeReadFailStore) SessionMode(id string) (string, error) {
	return "", fmt.Errorf("mode read failed")
}

// A persisted mode that cannot be read is a FAILED resume, not a quiet widening
// to build: falling back there would hand a plan-mode session the full toolset
// because the store blinked. The error surfaces instead.
func TestRestoreSession_FailsLoudlyWhenModeCannotBeRead(t *testing.T) {
	const id = "sess-unreadable-mode"
	db := newResumeFixture(t, id)

	// The transcript reads succeed via the embedded store; only the mode read
	// fails, so this exercises persistedMode's error return specifically.
	if _, err := restoreSession(&modeReadFailStore{DB: db}, id, "BASE PROMPT"); err == nil {
		t.Fatal("restoreSession() = nil error with a failing mode read; want a failed resume")
	}
}
