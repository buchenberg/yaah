package runner

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/tools"
)

// RoleWatcher polls the role search paths and reloads the default role
// registry whenever a role file changes outside the role tool. The role
// tool already reloads after its own mutations; this closes the known gap
// that raw file edits (a human in an editor, another tool) never reached
// the registry, so list_subagents and delegation kept serving stale roles
// for the rest of a long session.
//
// The scan is deliberate: it hashes the .md files under the search paths
// instead of watching inotify-style events. A handful of small files read
// once per interval is negligible, polling behaves identically on every
// platform (editors that save by rename, network filesystems), and it
// avoids a new direct dependency — the parity plan explicitly prices
// fsnotify against "cheap polling" and accepts either. Content hashing
// (not mtime) also sidesteps filesystem timestamp granularity: an edit
// that preserves size within the same timestamp tick still reloads.
//
// Refresh is registry-only, matching ReloadDefaultRoles semantics: new
// sub-agent dispatches pick up the changed registry immediately, in-flight
// sub-agents keep their already-loaded profiles, and the session's own
// persona and axes stay fixed. Reloads from the watcher and reloads from
// the role tool land on the same atomic swap, so overlap is idempotent.
type RoleWatcher struct {
	// Dirs are the role search paths, in the same form RoleSearchPaths
	// returns them. Missing directories contribute nothing until they
	// appear, which the scan then picks up as a change.
	Dirs []string
	// BuiltinFiles and Resolver mirror tools.RoleTool's fields: the
	// watcher replays the exact reload path the tool's mutations use.
	BuiltinFiles map[string][]byte
	Resolver     tools.RoleResolver

	// Interval is the scan period. Zero defaults to one second; tests set
	// something short.
	Interval time.Duration

	stop chan struct{}
	done chan struct{}

	// started and closed serialize the lifecycle and make both callers'
	// documented contracts real: a double Start must not launch a second
	// poll loop (the loops would both run and both close the shared done
	// channel — "close of closed channel"), and a Close before Start must
	// return immediately and also keep any later Start inert.
	mu      sync.Mutex
	started bool
	closed  bool
}

// NewRoleWatcher validates the minimum wiring and returns a stopped
// watcher. Resolver must be non-nil — a watcher that cannot reload is a
// no-op with an error, not a silent one.
func NewRoleWatcher(dirs []string, builtinFiles map[string][]byte, resolver tools.RoleResolver) (*RoleWatcher, error) {
	if resolver == nil {
		return nil, fmt.Errorf("role watcher: nil resolver")
	}
	return &RoleWatcher{
		Dirs:         dirs,
		BuiltinFiles: builtinFiles,
		Resolver:     resolver,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}, nil
}

// Start launches the scan loop. It returns immediately. Start is a no-op
// when already started, or when Close ran first — a closed watcher never
// runs. The loop does the initial reload itself, so a watcher started on
// an unmodified tree still leaves the registry exactly as it is.
func (w *RoleWatcher) Start() {
	w.mu.Lock()
	if w.started || w.closed {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.mu.Unlock()
	go w.loop()
}

// loop is the scan cycle; exactly one instance runs per watcher, launched
// by the Start that won the started race.
func (w *RoleWatcher) loop() {
	defer close(w.done)
	// first is the initial-scan marker: the first comparison always
	// runs against an empty snapshot, so the loop reloads once at
	// startup. That initial reload is harmless (the wiring already
	// loaded the same files) and keeps the changed/unchanged logic a
	// single path.
	first := true
	var prev map[string]string
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
		}
		cur := w.snapshot()
		if first || changed(prev, cur) {
			first = false
			// Store before reloading: a concurrent edit mid-reload
			// then shows up as a newer change on the next scan
			// instead of being swallowed by this one.
			prev = cur
			if err := w.Resolver.ReloadRoles(w.BuiltinFiles, w.Dirs); err != nil {
				fmt.Fprintf(os.Stderr, "warning: role watcher reload failed: %v\n", err)
			}
		}
	}
}

// Close stops the scan loop and waits for it to exit. Close is safe to
// call multiple times and from a teardown path that cannot know whether
// Start ever ran: before Start it marks the watcher closed and returns
// immediately (a later Start then stays inert); after Start it closes the
// stop channel and joins the loop. A no-op on an already-closed watcher.
func (w *RoleWatcher) Close() {
	w.mu.Lock()
	if w.closed {
		// Already joined or never started: done is closed by the first
		// Close's wait, and a never-started loop has nothing to join.
		w.mu.Unlock()
		if w.started {
			<-w.done
		}
		return
	}
	w.closed = true
	wasStarted := w.started
	if wasStarted {
		close(w.stop)
	}
	w.mu.Unlock()
	if wasStarted {
		<-w.done
	}
}

// snapshot hashes every .md file under the watched directories keyed by
// path. Files are read fully and hashed, so the signature is content, not
// metadata: the loop cannot miss an edit because timestamps collided. An
// unreadable file (vanished between directory read and open, permissions
// tightened mid-scan) contributes no entry at all — the snapshot treats it
// exactly like a deleted file, and a directory that turned unreadable
// drops every file it used to contribute, so those transitions are
// detected changes rather than silent freezes of the stored snapshot. This
// is also why no "" marker may be stored: an absent key and an unreadable
// file must compare equal or a failed create silently cancels a real
// deletion in the same scan.
func (w *RoleWatcher) snapshot() map[string]string {
	sig := make(map[string]string)
	seenDir := make(map[string]bool, len(w.Dirs))
	for _, dir := range w.Dirs {
		if seenDir[dir] {
			// RoleSearchPaths can yield the same directory twice (home
			// as an ancestor and as ~); scan it once.
			continue
		}
		seenDir[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // missing or unreadable directory: nothing to watch
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".md") {
				continue
			}
			if h := fileHash(filepath.Join(dir, name)); h != "" {
				sig[filepath.Join(dir, name)] = h
			}
		}
	}
	return sig
}

// fileHash reads the file and returns its hex SHA-256, or "" when the
// read fails (caller omits the entry — see snapshot).
func fileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// changed reports whether prev and cur differ in any watched file.
func changed(prev, cur map[string]string) bool {
	if len(prev) != len(cur) {
		return true
	}
	for path, sig := range cur {
		if prev[path] != sig {
			return true
		}
	}
	return false
}
