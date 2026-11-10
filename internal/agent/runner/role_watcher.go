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

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
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

// Start launches the scan loop. It returns immediately; Close shuts the
// loop down. Start twice is a no-op the second time — the loop does the
// initial reload itself, so a watcher started on an unmodified tree still
// leaves the registry exactly as it is.
func (w *RoleWatcher) Start() {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		defer close(w.done)
		// first is the initial-scan marker: the first comparison always
		// runs against an empty snapshot, so the loop reloads once at
		// startup. That initial reload is harmless (the wiring already
		// loaded the same files) and keeps the changed/unchanged logic a
		// single path.
		first := true
		var prev map[string]string
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
	}()
}

// Close stops the scan loop and waits for it to exit. Close is safe to
// call multiple times and from a teardown path that cannot know whether
// Start ever ran.
func (w *RoleWatcher) Close() {
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}

// snapshot hashes every .md file under the watched directories keyed by
// path. Files are read fully and hashed, so the signature is content, not
// metadata: the loop cannot miss an edit because timestamps collided, and
// a deleted file drops out of the map, which is itself a change.
func (w *RoleWatcher) snapshot() map[string]string {
	sig := make(map[string]string)
	for _, dir := range w.Dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // missing or unreadable directory: nothing to watch
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".md") {
				continue
			}
			path := filepath.Join(dir, name)
			if sig[path] != "" {
				// Duplicate dir entries share one path key.
				continue
			}
			sig[path] = fileHash(path)
		}
	}
	return sig
}

// fileHash reads the file and returns its hex SHA-256, or "" when the
// read fails (a file vanishing between ReadDir and read): "" keyed at the
// path differs from any content signature, so the next scan still sees
// the vanish as a change.
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
