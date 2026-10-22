package memory

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// blockingEmbedder signals when Embed has been entered and then blocks until
// the gate is released, so a test can pin an embed goroutine in flight across
// DB.Close.
type blockingEmbedder struct {
	started   chan struct{}
	gate      chan struct{}
	startOnce sync.Once
}

func newBlockingEmbedder() *blockingEmbedder {
	return &blockingEmbedder{started: make(chan struct{}), gate: make(chan struct{})}
}

func (b *blockingEmbedder) Embed(context.Context, string) (Embedding, error) {
	b.startOnce.Do(func() { close(b.started) })
	<-b.gate
	return Embedding{1, 0, 0}, nil
}

// waitFor blocks until the embed goroutine has entered Embed.
func (b *blockingEmbedder) waitFor(t *testing.T) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(2 * time.Second):
		t.Fatal("embed goroutine never started")
	}
}

// openDBAt opens the database at an explicit path (so a test can reopen the
// same file after Close) and creates the ses-test session.
func openDBAt(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	// A reopened file already holds the session; only create it once.
	if _, err := db.GetSession("ses-test"); err != nil {
		if err := db.CreateSession(Session{ID: "ses-test", StartedAt: time.Now().Unix()}); err != nil {
			db.Close()
			t.Fatalf("CreateSession: %v", err)
		}
	}
	return db
}

// countEmbedded reports how many rows in table with the given id carry a
// non-NULL embedding.
func countEmbedded(t *testing.T, db *DB, table, id string) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ? AND embedding IS NOT NULL`, id).Scan(&n); err != nil {
		t.Fatalf("count embedded %s rows: %v", table, err)
	}
	return n
}

// TestDB_CloseDrainsMessageEmbed pins the shutdown race found with
// `yaah acp-serve`: a prompt completes, the process exits, and Close used to
// close the SQL pool while the message embed goroutine was still in flight,
// losing the embedding with only a "database is closed" warning. Close must
// now wait for the in-flight embed to store its row.
func TestDB_CloseDrainsMessageEmbed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-msg.db")
	db := openDBAt(t, path)
	emb := newBlockingEmbedder()
	db.SetEmbedder(emb)

	if err := db.AddMessage(msg("drain-msg", "user", 0)); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	emb.waitFor(t) // the embed is now blocked inside Embed

	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()

	// Close must still be blocked: release the gate and let the write land.
	select {
	case err := <-closed:
		t.Fatalf("Close returned before the in-flight embed finished (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(emb.gate)

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the embed finished")
	}

	// Reopen the same file: the embedding must have landed before the pool
	// closed, instead of failing against it.
	db2 := openDBAt(t, path)
	defer db2.Close()
	if got := countEmbedded(t, db2, "messages", "drain-msg"); got != 1 {
		t.Errorf("messages.drain-msg with embedding = %d; want 1", got)
	}
}

// TestDB_CloseDrainsMemoryEmbed is the same guarantee for the memory_add
// path (EmbedMemoryAsync).
func TestDB_CloseDrainsMemoryEmbed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-mem.db")
	db := openDBAt(t, path)
	emb := newBlockingEmbedder()
	db.SetEmbedder(emb)

	if err := db.AddMemory(Entry{ID: "drain-mem", Text: "remember this", CreatedAt: 1}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	done := db.EmbedMemoryAsync("drain-mem", "remember this")
	if done == nil {
		t.Fatal("EmbedMemoryAsync returned nil with an embedder configured")
	}
	emb.waitFor(t)

	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before the in-flight embed finished (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(emb.gate)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("embed done channel never closed")
	}
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2 := openDBAt(t, path)
	defer db2.Close()
	if got := countEmbedded(t, db2, "memory", "drain-mem"); got != 1 {
		t.Errorf("memory.drain-mem with embedding = %d; want 1", got)
	}
}

// TestDB_EmbedAfterCloseSkips pins the other half of the gate: work started
// after Close must be skipped rather than racing the closed pool.
func TestDB_EmbedAfterCloseSkips(t *testing.T) {
	db := openTestDB(t)
	db.SetEmbedder(newBlockingEmbedder())

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	done := db.EmbedMemoryAsync("late-mem", "too late")
	if done == nil {
		t.Fatal("EmbedMemoryAsync returned nil; the embedder is configured")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("embed registered after Close; the channel should be closed immediately")
	}
	if msgCh := db.embedMessageAsync("late-msg", "user", "too late"); msgCh != nil {
		select {
		case <-msgCh:
		case <-time.After(time.Second):
			t.Fatal("message embed registered after Close")
		}
	}
}

func TestPendingEmbeds_DrainTimeout(t *testing.T) {
	var p pendingEmbeds
	finish, ok := p.begin(func() {})
	if !ok {
		t.Fatal("begin() refused on a fresh pendingEmbeds")
	}
	defer finish()

	if p.drain(20 * time.Millisecond) {
		t.Fatal("drain reported success while an embed was still in flight")
	}
	if _, ok := p.begin(func() {}); ok {
		t.Fatal("begin() accepted work after drain closed the gate")
	}
}

func TestPendingEmbeds_DrainWaits(t *testing.T) {
	var p pendingEmbeds
	finish, ok := p.begin(func() {})
	if !ok {
		t.Fatal("begin() refused on a fresh pendingEmbeds")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		finish()
	}()

	if !p.drain(2 * time.Second) {
		t.Fatal("drain timed out waiting for a finishing embed")
	}
}

// TestPendingEmbeds_DrainCancels pins the bounded-shutdown fix for slow
// embedding servers: drain cancels the registered embeds' contexts
// before waiting, so a 30s HTTP embed cannot keep racing the pool past
// a timed-out wait.
func TestPendingEmbeds_DrainCancels(t *testing.T) {
	var p pendingEmbeds
	cancelled := make(chan struct{})
	finish, ok := p.begin(func() { close(cancelled) })
	if !ok {
		t.Fatal("begin() refused on a fresh pendingEmbeds")
	}
	// Mirror the real embed goroutine: unwind (and unregister) once the
	// context is cancelled.
	go func() {
		<-cancelled
		finish()
	}()
	if !p.drain(2 * time.Second) {
		t.Fatal("drain timed out with no in-flight work")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("drain did not cancel the registered embed's context")
	}
}
