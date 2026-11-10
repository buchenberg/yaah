package runner

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tools"
)

// countingResolver implements tools.RoleResolver for watcher tests: it
// counts ReloadRoles calls and can fail them, so tests observe reloads
// without a real registry.
type countingResolver struct {
	mu    sync.Mutex
	loads int
	err   error
}

func (r *countingResolver) RoleNames() []string          { return nil }
func (r *countingResolver) ListRoles() []tools.RoleEntry { return nil }

func (r *countingResolver) ReloadRoles(_ map[string][]byte, _ []string) error {
	r.mu.Lock()
	r.loads++
	r.mu.Unlock()
	return r.err
}

func (r *countingResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loads
}

// waitFor polls until the count moves past from, or fails the test; the
// tick is small enough that this stays fast while tolerating scheduler
// jitter.
func (r *countingResolver) waitFor(t *testing.T, from int) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n := r.count(); n > from {
			return n
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("reload count stuck at %d; want >%d", r.count(), from)
	return 0
}

func TestRoleWatcher_NilResolver(t *testing.T) {
	if _, err := NewRoleWatcher(nil, nil, nil); err == nil {
		t.Fatal("NewRoleWatcher(nil resolver) = nil error; want error")
	}
}

func TestRoleWatcher_ReloadOnFileChangeAndDelete(t *testing.T) {
	dir := t.TempDir()
	r := &countingResolver{}
	w, err := NewRoleWatcher([]string{filepath.Join(dir, "missing-subdir"), dir}, nil, r)
	if err != nil {
		t.Fatalf("NewRoleWatcher: %v", err)
	}
	w.Interval = 10 * time.Millisecond
	w.Start()
	defer w.Close()

	// The first scan always compares against an empty snapshot, so a
	// directory holding an .md file at startup reloads once even without
	// an edit.
	if n := r.waitFor(t, 0); n < 1 {
		t.Fatalf("initial reload count = %d; want >=1", n)
	}
	afterInitial := r.count()

	// A raw edit the role tool did not make must still reload.
	path := filepath.Join(dir, "watched.md")
	if err := os.WriteFile(path, []byte("---\ndescription: watched\n---\nbody"), 0o644); err != nil {
		t.Fatalf("write role file: %v", err)
	}
	afterCreate := r.waitFor(t, afterInitial)

	// Deleting the same file is a change too.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove role file: %v", err)
	}
	r.waitFor(t, afterCreate)
}

func TestRoleWatcher_IgnoresNonRoleFiles(t *testing.T) {
	dir := t.TempDir()
	r := &countingResolver{}
	w, err := NewRoleWatcher([]string{dir}, nil, r)
	if err != nil {
		t.Fatalf("NewRoleWatcher: %v", err)
	}
	w.Interval = 10 * time.Millisecond
	w.Start()
	defer w.Close()

	base := r.waitFor(t, 0) // initial scan reload
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("zz"), 0o644); err != nil {
		t.Fatalf("write non-role file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Let several intervals pass: the scan must treat both as noise.
	time.Sleep(60 * time.Millisecond)
	if n := r.count(); n != base {
		t.Fatalf("non-.md changes reloaded: got %d, want %d", n, base)
	}
}

func TestRoleWatcher_CloseStopsReloads(t *testing.T) {
	dir := t.TempDir()
	r := &countingResolver{}
	w, err := NewRoleWatcher([]string{dir}, nil, r)
	if err != nil {
		t.Fatalf("NewRoleWatcher: %v", err)
	}
	w.Interval = 10 * time.Millisecond
	w.Start()
	r.waitFor(t, 0)
	w.Close()

	base := r.count()
	path := filepath.Join(dir, "late.md")
	if err := os.WriteFile(path, []byte("late"), 0o644); err != nil {
		t.Fatalf("write after close: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if n := r.count(); n != base {
		t.Fatalf("reload after Close: got %d, want %d", n, base)
	}
	// Second Close must be a no-op, not a double close panic.
	w.Close()
}
