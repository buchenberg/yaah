package yaah

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/types"
)

// TestEndSessionRowIsIdempotent pins the one write a session makes on its way
// out (yaah-cqxg). The seam that failed is process exit, which no test can
// shell out to, so the write is a function of its own and this drives it twice:
// the row must record the exit once, never shrink its usage, and never reopen
// itself — a second close arrives whenever a signal funnel races the driver's
// deferred close.
func TestEndSessionRowIsIdempotent(t *testing.T) {
	db, path := openStateDB(t)
	if err := db.CreateSession(memory.Session{ID: "sess-exit", StartedAt: 1000}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	total := types.Usage{PromptTokens: 500, CompletionTokens: 300, CachedPromptTokens: 120}
	sub := types.Usage{PromptTokens: 200, CompletionTokens: 100}

	if err := endSessionRow(db, "sess-exit", 2000, total, sub); err != nil {
		t.Fatalf("endSessionRow: %v", err)
	}
	// The second close carries a later clock and a stale, smaller snapshot.
	stale := types.Usage{PromptTokens: 10, CompletionTokens: 5}
	if err := endSessionRow(db, "sess-exit", 3000, stale, types.Usage{}); err != nil {
		t.Fatalf("second endSessionRow: %v", err)
	}
	// A third with a zero clock and no usage: the row must not reopen.
	if err := endSessionRow(db, "sess-exit", 0, types.Usage{}, types.Usage{}); err != nil {
		t.Fatalf("third endSessionRow: %v", err)
	}

	row := readSession(t, path, "sess-exit")
	if row.EndedAt != 3000 {
		t.Errorf("ended_at = %d; want 3000 (a close advances it, a zero clock must not reopen it)", row.EndedAt)
	}
	if row.TokensIn != 500 || row.TokensOut != 300 || row.TokensCached != 120 {
		t.Errorf("tokens = %d/%d/%d; want 500/300/120 (a stale close must not shrink usage)",
			row.TokensIn, row.TokensOut, row.TokensCached)
	}
	if row.SubTokensIn != 200 || row.SubTokensOut != 100 {
		t.Errorf("sub tokens = %d/%d; want 200/100", row.SubTokensIn, row.SubTokensOut)
	}
}

// TestEndSessionRowReportsAnUnknownSession pins the silent-no-op guard: a close
// whose UPDATE matches no row is the exact shape of the reported bug (a
// finished session still listed as open), so it must be an error, not success.
func TestEndSessionRowReportsAnUnknownSession(t *testing.T) {
	db, _ := openStateDB(t)
	if err := endSessionRow(db, "sess-nonesuch", 2000, types.Usage{}, types.Usage{}); err == nil {
		t.Error("endSessionRow for an unknown session returned nil; the caller cannot tell the row was never written")
	}
}

// TestSessionCloseWritesTheRowOnce pins close() as the single idempotent exit:
// the first call makes the session's end durable, and a second call — the
// signal funnel arriving after the deferred close, or the reverse — tears
// nothing down twice and writes nothing to the (now closed) store, so it warns
// about nothing.
func TestSessionCloseWritesTheRowOnce(t *testing.T) {
	db, path := openStateDB(t)
	if err := db.CreateSession(memory.Session{ID: "sess-twice", StartedAt: 1000}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess := &agentSession{db: db, sessionID: "sess-twice"}
	sess.totalUsage = types.Usage{PromptTokens: 40, CompletionTokens: 2}
	sess.subUsage = types.Usage{PromptTokens: 1, CompletionTokens: 1}

	// Capture stderr: a re-run close reaches a closed store and would warn.
	saved := os.Stderr
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatalf("pipe: %v", perr)
	}
	os.Stderr = w
	sess.close()
	sess.close()
	os.Stderr = saved
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if buf.Len() > 0 {
		t.Errorf("the second close tore down again: %s", buf.String())
	}

	row := readSession(t, path, "sess-twice")
	if row.EndedAt == 0 {
		t.Fatal("ended_at = 0 after close; the exit write never landed")
	}
	if row.TokensIn != 40 || row.TokensOut != 2 || row.SubTokensIn != 1 {
		t.Errorf("usage = %d/%d sub %d; want 40/2 sub 1", row.TokensIn, row.TokensOut, row.SubTokensIn)
	}
}

// TestTerminationSignalClosesSessionThenExits pins the funnel's ordering: the
// session is closed BEFORE the process exits, which is the whole fix — a
// process that exits first leaves the row open, which is the reported symptom.
// Driving the extracted body with a channel keeps the test off real signals.
func TestTerminationSignalClosesSessionThenExits(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	var order []string
	code := -1
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTerminationSignal(sigs,
			func() { order = append(order, "close") },
			func(c int) { order = append(order, "exit"); code = c })
	}()

	sigs <- os.Interrupt
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the funnel did not return after a signal")
	}

	if len(order) != 2 || order[0] != "close" || order[1] != "exit" {
		t.Fatalf("actions = %v; want [close exit] — the row must be written before the process dies", order)
	}
	if code != interruptExitCode {
		t.Errorf("exit code = %d; want %d", code, interruptExitCode)
	}
}

// TestTerminationFunnelStopIsSilent pins the driver's stop: uninstalling the
// funnel closes its channel without a signal, and that must close nothing and
// exit nothing — the normal return path belongs to the caller's own close.
func TestTerminationFunnelStopIsSilent(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	closed, exited := false, false
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTerminationSignal(sigs,
			func() { closed = true },
			func(int) { exited = true })
	}()
	close(sigs)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the funnel did not return when its channel was closed")
	}
	if closed || exited {
		t.Errorf("closed=%v exited=%v; a stopped funnel must do neither", closed, exited)
	}
}

// openStateDB opens a private session store and returns it with its path, so a
// test can read a row back after the session closed the handle.
func openStateDB(t *testing.T) (*memory.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := memory.Open(path)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	// An open handle blocks the TempDir cleanup on Windows; the session under
	// test closes its own store, so a second Close here is a plain no-op.
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// readSession reads one session row through a fresh handle: the session under
// test owns (and closes) the store it wrote through.
func readSession(t *testing.T, path, id string) memory.Session {
	t.Helper()
	db, err := memory.Open(path)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer db.Close()
	row, err := db.GetSession(id)
	if err != nil {
		t.Fatalf("GetSession(%s): %v", id, err)
	}
	return row
}
