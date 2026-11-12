package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// editCall runs one single-hunk edit through the tool's public Execute.
func editCall(et *EditTool, filePath, oldStr, newStr string) (string, error) {
	args, err := json.Marshal(map[string]any{
		"filePath":  filePath,
		"oldString": oldStr,
		"newString": newStr,
	})
	if err != nil {
		return "", err
	}
	return et.Execute(context.Background(), string(args))
}

// multiEditCall runs one edits[] call through the tool's public Execute.
func multiEditCall(et *EditTool, filePath string, edits ...editEntry) (string, error) {
	args, err := json.Marshal(map[string]any{"filePath": filePath, "edits": edits})
	if err != nil {
		return "", err
	}
	return et.Execute(context.Background(), string(args))
}

// boundedWait fails the test instead of hanging when a handshake never fires.
func boundedWait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never happened; the per-path lock leaked or deadlocked", what)
	}
}

// boundedResult is boundedWait for a one-shot error channel.
func boundedResult(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never finished; the per-path lock leaked or deadlocked", what)
		return nil
	}
}

// TestEditGuard_LostUpdateIsFixed pins the bug yaah-qlli exists for: two (or
// eight) mutating edits issued in one tool batch used to race
// read-modify-write cycles on the same file, and the loser's write silently
// discarded the winner's. Every goroutine appends one unique marker by
// replacing the shared sentinel line; under the per-path write guard each
// edit sees the previous edit's result, so all markers survive. Barrier-then-
// batch is the shape that lost updates before the guard.
func TestEditGuard_LostUpdateIsFixed(t *testing.T) {
	const goroutines = 8
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "markers.txt")
	if err := os.WriteFile(path, []byte("base\n// markers\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	et := &EditTool{WS: ws}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			marker := fmt.Sprintf("// marker-%02d\n", i)
			<-start
			_, errs[i] = editCall(et, path, "// markers", "// markers\n"+marker)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("edit %d failed: %v", i, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < goroutines; i++ {
		if !strings.Contains(string(data), fmt.Sprintf("// marker-%02d\n", i)) {
			t.Errorf("marker %d was lost to a concurrent edit — the per-path guard is not serializing writes; content:\n%s", i, data)
		}
	}
}

// staleRacingWorkspace wraps the real local workspace: its first ReadFile
// returns the real bytes and, before returning, an out-of-band writer
// (another process, an editor) replaces the file. Later reads observe the
// replacement — exactly the race the guard's compare-and-swap exists to
// catch, because the in-process lock cannot see another process.
type staleRacingWorkspace struct {
	Workspace
	replace []byte

	once    sync.Once
	raceErr error
}

func (w *staleRacingWorkspace) ReadFile(ctx context.Context, p string) ([]byte, error) {
	data, err := w.Workspace.ReadFile(ctx, p)
	if err != nil {
		return data, err
	}
	w.once.Do(func() { w.raceErr = os.WriteFile(p, w.replace, 0o644) })
	return data, err
}

// TestEditGuard_RefusesStaleWrite pins the compare-and-swap half of the
// guard: when the file changes between the edit's read and its write, the
// edit fails with *StaleWriteError and the file keeps the out-of-band bytes —
// the edit's own result is never written on top of someone else's change.
func TestEditGuard_RefusesStaleWrite(t *testing.T) {
	real, dir := realWorkspace(t)
	path := filepath.Join(dir, "stale.txt")
	if err := os.WriteFile(path, []byte("sentinel line\ntrail\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outOfBand := "rewritten by another writer\n"
	ws := &staleRacingWorkspace{Workspace: real, replace: []byte(outOfBand)}
	et := &EditTool{WS: ws}

	_, err := editCall(et, path, "sentinel line", "edited line")
	var stale *StaleWriteError
	if !errors.As(err, &stale) {
		t.Fatalf("edit error = %v, want *StaleWriteError", err)
	}
	if ws.raceErr != nil {
		t.Fatalf("out-of-band write failed: %v", ws.raceErr)
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(data) != outOfBand {
		t.Errorf("file = %q, want the out-of-band bytes; a refused edit must write nothing", data)
	}
}

// TestEditGuard_MissingFileFailsLikeBefore pins the plumbing the conversion
// had to keep: a missing file is the workspace read error wrapped in
// "edit: " with fs.ErrNotExist intact — not a misleading "oldString not
// found" for a file that does not exist.
func TestEditGuard_MissingFileFailsLikeBefore(t *testing.T) {
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "absent.txt")
	et := &EditTool{WS: ws}

	_, err := editCall(et, path, "anything", "nothing")
	if err == nil {
		t.Fatal("editing a missing file must fail")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want fs.ErrNotExist detectable through the wrap", err)
	}
	if !strings.HasPrefix(err.Error(), "edit: ") {
		t.Errorf("error = %q, want the historical \"edit: \" prefix", err)
	}
}

// serializingWorkspace records the bytes every ReadFile returns and parks the
// very first read until released — while that caller holds the per-path
// write lock — so the test can prove a second edit neither observes nor
// writes anything until the first edit completes.
type serializingWorkspace struct {
	Workspace
	entered chan struct{}
	release chan struct{}

	once sync.Once
	mu   sync.Mutex
	seen [][]byte
}

func (w *serializingWorkspace) ReadFile(ctx context.Context, p string) ([]byte, error) {
	data, err := w.Workspace.ReadFile(ctx, p)
	if err != nil {
		return data, err
	}
	w.mu.Lock()
	w.seen = append(w.seen, append([]byte(nil), data...))
	w.mu.Unlock()
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	return data, err
}

func (w *serializingWorkspace) snapshots() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.seen...)
}

// TestEditGuard_SerializesSamePathEdits pins the lock half of the guard:
// while one edit is parked inside its critical section, a second edit on the
// same path cannot observe the file at all, and once it runs it sees only
// fully-formed states — the original or the first edit's complete result —
// never a torn or half-applied one.
func TestEditGuard_SerializesSamePathEdits(t *testing.T) {
	real, dir := realWorkspace(t)
	path := filepath.Join(dir, "serial.txt")
	original := "head\nshared sentinel\ntail\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &serializingWorkspace{
		Workspace: real,
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	et := &EditTool{WS: ws}

	// The first edit parks inside the guard's read, holding the per-path
	// write lock open.
	firstDone := make(chan error, 1)
	go func() {
		_, err := editCall(et, path, "shared sentinel", "shared sentinel\nfirst edit body")
		firstDone <- err
	}()
	boundedWait(t, ws.entered, "first edit entered its guarded read")

	// A second edit on the same path must not observe or write anything while
	// the first is parked inside its critical section.
	secondDone := make(chan error, 1)
	go func() {
		_, err := editCall(et, path, "first edit body", "first edit body\nsecond edit body")
		secondDone <- err
	}()
	yield()
	if n := len(ws.snapshots()); n != 1 {
		t.Fatalf("second edit recorded %d reads while the first edit held the per-path lock; want 1", n)
	}

	close(ws.release)
	if err := boundedResult(t, firstDone, "first edit"); err != nil {
		t.Fatalf("first edit failed: %v", err)
	}
	if err := boundedResult(t, secondDone, "second edit"); err != nil {
		t.Fatalf("second edit failed: %v", err)
	}

	// Both edits read exactly twice (before and after their mutate). Every
	// observed state must be fully formed: the original file, or the first
	// edit's complete result.
	afterFirst := "head\nshared sentinel\nfirst edit body\ntail\n"
	snaps := ws.snapshots()
	if len(snaps) != 4 {
		t.Fatalf("recorded %d reads, want 4 (two per guarded edit)", len(snaps))
	}
	for i, snap := range snaps {
		if string(snap) != original && string(snap) != afterFirst {
			t.Errorf("read %d observed a torn state, neither the original nor the first edit's complete result:\n%q", i, snap)
		}
	}
	// The second edit must have built on the first edit's complete result.
	for i, snap := range snaps[2:] {
		if string(snap) != afterFirst {
			t.Errorf("second edit's read %d = %q, want the first edit's complete result", i, snap)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "head\nshared sentinel\nfirst edit body\nsecond edit body\ntail\n"; string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
}

// TestEditGuard_MultiEditThroughGuard exercises the multi-edit path through
// the guard: independent hunks land in one guarded write, and a hunk that no
// longer matches reports the usual all-edits-failed wording.
func TestEditGuard_MultiEditThroughGuard(t *testing.T) {
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "multi.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	et := &EditTool{WS: ws}

	msg, err := multiEditCall(et, path,
		editEntry{OldString: "alpha", NewString: "ALPHA"},
		editEntry{OldString: "gamma", NewString: "GAMMA"},
	)
	if err != nil {
		t.Fatalf("two-hunk multi-edit failed: %v", err)
	}
	if strings.Contains(msg, "failed") {
		t.Errorf("a fully applied multi-edit must not report failures: %q", msg)
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if want := "ALPHA\nbeta\nGAMMA\n"; string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}

	_, err = multiEditCall(et, path, editEntry{OldString: "alpha", NewString: "nope"})
	if err == nil {
		t.Fatal("a multi-edit whose only hunk no longer matches must fail")
	}
	if !strings.Contains(err.Error(), "all 1 edits failed") || !strings.Contains(err.Error(), "edit #0: oldString not found") {
		t.Errorf("error = %q, want the usual all-edits-failed wording", err)
	}
}
