package yaah

import (
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

// A restore is not a switch: the wiring sets the session's mode field directly,
// so a session that merely came back in plan mode announces nothing — its
// directive already states the regime, and no switch happened.
func TestRestoredModeIsNotAnnounced(t *testing.T) {
	sess := &agentSession{mode: acpModePlan}
	view := &safeCaptureView{}
	sess.SetView(view)

	if got := sess.Mode(); got != acpModePlan {
		t.Fatalf("Mode() = %q; want %q", got, acpModePlan)
	}
	if len(view.events) != 0 {
		t.Fatalf("view received %d event(s) for a restored mode; want 0", len(view.events))
	}
}
