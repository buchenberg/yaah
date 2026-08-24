package agent

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/types"
)

func newPersistTestDB(t *testing.T, sessionID string) *memory.DB {
	t.Helper()
	db, err := memory.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSession(memory.Session{
		ID: sessionID, StartedAt: time.Now().Unix(), CWD: "/tmp", Model: "test",
	}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	return db
}

// TestSessionPersister_Rebase pins the compaction resync (review B7):
// after a wholesale message replacement the DB holds exactly the new
// baseline and the cursor continues from its length.
func TestSessionPersister_Rebase(t *testing.T) {
	db := newPersistTestDB(t, "sess-rb")
	p := NewSessionPersister(db, nil, "sess-rb")

	for _, c := range []string{"one", "two", "three", "four", "five"} {
		p.Persist(types.Message{Role: "user", Content: c})
	}
	if p.MsgIdx() != 5 {
		t.Fatalf("MsgIdx after 5 persists = %d", p.MsgIdx())
	}

	// Compaction replaces the conversation with a 2-message baseline.
	baseline := []types.Message{
		types.SystemMsg("summary of earlier discussion"),
		types.UserMsg("five"),
	}
	p.Rebase(baseline)

	if p.MsgIdx() != 2 {
		t.Errorf("MsgIdx after rebase = %d; want 2", p.MsgIdx())
	}
	msgs, err := db.GetMessages("sess-rb")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("rows after rebase = %d; want 2", len(msgs))
	}
	if msgs[0].Content != "summary of earlier discussion" || msgs[1].Content != "five" {
		t.Errorf("rebased rows = %q, %q", msgs[0].Content, msgs[1].Content)
	}

	// New messages after the rebase append at the right positions.
	p.Persist(types.AssistantMsg("six", nil))
	msgs, err = db.GetMessages("sess-rb")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("rows after post-rebase persist = %d; want 3", len(msgs))
	}
	if msgs[2].Content != "six" || msgs[2].Idx != 2 {
		t.Errorf("post-rebase row = idx=%d content=%q", msgs[2].Idx, msgs[2].Content)
	}
}

// TestSessionPersister_RebaseNilDB ensures rebase keeps cursor semantics
// when persistence is disabled.
func TestSessionPersister_RebaseNilDB(t *testing.T) {
	p := NewSessionPersister(nil, nil, "sess-nil")
	p.Persist(types.UserMsg("x"))
	p.Rebase([]types.Message{types.SystemMsg("s"), types.UserMsg("u")})
	if p.MsgIdx() != 2 {
		t.Errorf("MsgIdx = %d; want 2", p.MsgIdx())
	}
}

// TestSessionPersister_TruncateFrom pins the turn-restore behavior
// (review B7): rolled-back rows are deleted, not merely orphaned.
func TestSessionPersister_TruncateFrom(t *testing.T) {
	db := newPersistTestDB(t, "sess-tr")
	p := NewSessionPersister(db, nil, "sess-tr")

	for _, c := range []string{"a", "b", "c", "d"} {
		p.Persist(types.Message{Role: "user", Content: c})
	}

	// Rewind to 2 messages (the failed turn's rows must vanish).
	p.TruncateFrom(2)

	if p.MsgIdx() != 2 {
		t.Errorf("MsgIdx after truncate = %d; want 2", p.MsgIdx())
	}
	msgs, err := db.GetMessages("sess-tr")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("rows after truncate = %d; want 2", len(msgs))
	}
	if msgs[0].Content != "a" || msgs[1].Content != "b" {
		t.Errorf("surviving rows = %q, %q", msgs[0].Content, msgs[1].Content)
	}

	// A resumed session re-persists from the cursor without conflicts.
	p.Persist(types.UserMsg("e"))
	msgs, err = db.GetMessages("sess-tr")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || msgs[2].Content != "e" {
		t.Fatalf("rows after resume persist = %d", len(msgs))
	}
}

// TestLoop_applyDefaultsWiresRebase pins that the Loop wires the
// ContextManager replacement hook to persistence rebasing.
func TestLoop_applyDefaultsWiresRebase(t *testing.T) {
	db := newPersistTestDB(t, "sess-wire")
	loop := &Loop{
		Config:    LoopConfig{SessionID: "sess-wire"},
		Persister: NewSessionPersister(db, nil, "sess-wire"),
	}
	loop.applyDefaults()
	if loop.CtxMgr == nil || loop.CtxMgr.OnMessagesReplaced == nil {
		t.Fatal("applyDefaults did not wire OnMessagesReplaced")
	}

	loop.Persister.Persist(types.UserMsg("old"))
	loop.CtxMgr.OnMessagesReplaced([]types.Message{types.SystemMsg("new baseline")})

	msgs, err := db.GetMessages("sess-wire")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "new baseline" {
		t.Fatalf("rows after wired rebase = %d (first content %q); want exactly [new baseline]",
			len(msgs), firstContent(msgs))
	}
}

func firstContent(msgs []memory.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[0].Content
}
