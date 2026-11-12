package tools

// The per-path file lock and the file write guard live at the TOOL BOUNDARY —
// in this file and in the tools that call it — and never inside a shared
// Workspace or write helper.
//
// lockConflictPath is refcounted but NOT reentrant per goroutine: a second
// acquisition by the same goroutine blocks forever on itself. Multi-path
// tools (replace, bulk_files) loop over many files calling inner write
// primitives, so if this lock were taken inside a shared lower layer — a
// Workspace write method, a shared mutate helper — one goroutine would
// deadlock on its own second acquisition the moment an outer tool wrapped a
// loop of writes around it. Outer multi-path tools therefore acquire
// lockConflictPathSet / withFileWriteSet ONCE over their whole path set and
// then call unlocked inner primitives; single-path tools take withFileWrite
// (or lockConflictPath) once per file. No layer in between may re-acquire.
//
// The lock is in-process only: it serializes goroutines within one yaah
// process, not other processes, editors, or a second yaah instance writing
// the same tree. That is why withFileWrite layers a compare-and-swap digest
// check on top of it — the CAS is the correctness guarantee against the
// outside world; the lock is the ordering guarantee inside one process.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"
)

type conflictPathLock struct {
	mu   sync.Mutex
	refs int
}

var (
	conflictPathLocksMu sync.Mutex
	conflictPathLocks   = make(map[string]*conflictPathLock)
)

// lockConflictPath serializes snapshot/write/snapshot for one path. The key is
// case-folded on case-insensitive hosts, matching the conflict comparison.
// Entries are refcounted and removed after the last holder releases, so a
// long-lived session does not retain one mutex per path it ever touched.
func lockConflictPath(abs string) func() {
	key := pathKey(normalizeTrackedPath(abs))
	conflictPathLocksMu.Lock()
	lock := conflictPathLocks[key]
	if lock == nil {
		lock = &conflictPathLock{}
		conflictPathLocks[key] = lock
	}
	lock.refs++
	conflictPathLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		conflictPathLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(conflictPathLocks, key)
		}
		conflictPathLocksMu.Unlock()
	}
}

// lockConflictPathSet takes the per-path lock for every named path, in sorted
// key order, and returns one release. Sorted acquisition is what keeps two
// dispatches that touch the same pair of paths in opposite directions from
// deadlocking.
func lockConflictPathSet(paths ...string) func() {
	keys := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		key := pathKey(normalizeTrackedPath(p))
		if seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	sort.Strings(keys)
	unlocks := make([]func(), 0, len(keys))
	for _, key := range keys {
		unlocks = append(unlocks, lockConflictPath(key))
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
}

// FileWriteReceipt reports one completed file write.
type FileWriteReceipt struct {
	// Path is the workspace-relative display path (absolute when the write
	// landed outside the workspace root), matching the conflict tracker's
	// path rendering.
	Path string
	// BeforeBytes is the file's size before the write, or -1 when the path
	// did not exist.
	BeforeBytes int
	// AfterBytes is the size of the written content.
	AfterBytes int
	// Digest is the sha256 hex digest of the post-write content.
	Digest string
	// Created reports that the path did not exist before the write.
	Created bool
}

// String renders the receipt as one stable line, path first: the write verb,
// the byte delta, and a short digest prefix.
func (r FileWriteReceipt) String() string {
	verb := "wrote"
	before := r.BeforeBytes
	if r.Created {
		verb = "created"
		before = 0
	}
	return fmt.Sprintf("%s %s (%d -> %d bytes, sha256:%s)", verb, r.Path, before, r.AfterBytes, shortFileDigest(r.Digest))
}

// shortFileDigest trims a full sha256 hex digest to the 12-character prefix
// used in receipt lines, tolerating a shorter or empty input.
func shortFileDigest(hex string) string {
	if len(hex) > 12 {
		return hex[:12]
	}
	return hex
}

// StaleWriteError reports that the file changed between the tool's read and
// its write; the write was refused and the file is untouched. Expected and
// Found are full 64-character sha256 hex digests of the content observed at
// the read and again after mutate ran; a side that did not exist (a file
// created or deleted out of band) is represented by the digest of an empty
// read, so the message always carries full digests.
type StaleWriteError struct{ Path, Expected, Found string }

func (e *StaleWriteError) Error() string {
	return fmt.Sprintf("%s: file changed by another writer (expected %s, found %s); re-read and retry", e.Path, e.Expected, e.Found)
}

// withFileWrite runs mutate inside the per-path write critical section: it
// reads the current content under the lock, calls mutate, verifies the file
// did not change while mutate ran, then writes the result and returns a
// receipt. An out-of-band change between the read and the write is reported
// as *StaleWriteError and leaves the file untouched (nothing written).
//
// abs must already be resolved (ResolvePath / ResolveWritePath): the guard
// does no containment checking of its own. For a missing file mutate receives
// a nil cur and mode 0o644; for an existing file it receives the file's bytes
// and permission bits. The write uses that same mode — mutate's signature
// carries the mode inward only — defaulting a zero mode to 0o644 (on the
// local lane an existing file's mode is preserved regardless, per
// AtomicWriteFile). If mutate returns an error no write happens and that
// error is returned wrapped, with the cause reachable through errors.As.
// Creating missing parent directories is the caller's concern; on the local
// lane WriteFile happens to create them.
//
// The per-path lock is taken here, at the tool boundary — never inside a
// shared Workspace or write helper — because it is not reentrant per
// goroutine: a lower-layer acquisition would deadlock any outer tool that
// loops over files. Mutate must not call back into withFileWrite for the same
// path.
func withFileWrite(ctx context.Context, ws Workspace, abs string, mutate func(cur []byte, mode fs.FileMode) ([]byte, error)) (FileWriteReceipt, error) {
	unlock := lockConflictPath(abs)
	defer unlock()

	before, cur, err := fileGuardSnapshot(ctx, ws, abs)
	if err != nil {
		return FileWriteReceipt{}, err
	}

	next, err := mutate(cur, before.Mode)
	if err != nil {
		return FileWriteReceipt{}, fmt.Errorf("%s: %w", conflictWorkspaceRel(ws, abs), err)
	}
	// mutate cannot change the mode (its signature carries mode inward only),
	// so the write uses the mode the guard read: the existing file's
	// permission bits, or 0o644 for a path that did not exist. A zero mode
	// (a permission-less existing file) defaults the same way.
	mode := before.Mode
	if mode == 0 {
		mode = 0o644
	}

	// Compare-and-swap: the lock only orders goroutines in this process, so
	// anything mutate let race past it — another process, an editor — is
	// caught here, before a byte is written.
	after, _, err := fileGuardSnapshot(ctx, ws, abs)
	if err != nil {
		return FileWriteReceipt{}, err
	}
	if after.Exists != before.Exists || after.Digest != before.Digest {
		return FileWriteReceipt{}, &StaleWriteError{
			Path:     conflictWorkspaceRel(ws, abs),
			Expected: before.Digest,
			Found:    after.Digest,
		}
	}

	if err := ws.WriteFile(ctx, abs, next, mode); err != nil {
		return FileWriteReceipt{}, fmt.Errorf("%s: %w", conflictWorkspaceRel(ws, abs), err)
	}
	return FileWriteReceipt{
		Path:        conflictWorkspaceRel(ws, abs),
		BeforeBytes: before.Bytes,
		AfterBytes:  len(next),
		Digest:      proposalContentDigest(next),
		Created:     !before.Exists,
	}, nil
}

// withFileWriteSet takes the per-path lock for every path (sorted, single
// release) and then runs fn once — the shape multi-path tools (replace,
// bulk_files) need so one goroutine never re-enters the non-reentrant lock.
// fn loops over its files calling unlocked inner primitives (withFileWrite is
// fine for paths NOT in the set; the set's own paths must not be re-locked).
// fn's error is returned unchanged.
func withFileWriteSet(paths []string, fn func() error) error {
	unlock := lockConflictPathSet(paths...)
	defer unlock()
	return fn()
}

// fileGuardSnapshot reads one path's guard state: existence, permission
// bits, size, and content digest. A missing path reports Exists false, a nil
// content slice, mode 0o644, Bytes -1, and the digest of an empty read, so
// the CAS can compare a created-or-deleted file against a never-blank digest.
type fileGuardState struct {
	Exists bool
	Mode   fs.FileMode
	Bytes  int
	Digest string
}

func fileGuardSnapshot(ctx context.Context, ws Workspace, abs string) (fileGuardState, []byte, error) {
	info, err := ws.Lstat(ctx, abs)
	if errors.Is(err, fs.ErrNotExist) {
		return fileGuardState{Mode: 0o644, Bytes: -1, Digest: proposalContentDigest(nil)}, nil, nil
	}
	if err != nil {
		return fileGuardState{}, nil, fmt.Errorf("%s: %w", conflictWorkspaceRel(ws, abs), err)
	}
	cur, err := ws.ReadFile(ctx, abs)
	if err != nil {
		return fileGuardState{}, nil, fmt.Errorf("%s: %w", conflictWorkspaceRel(ws, abs), err)
	}
	return fileGuardState{Exists: true, Mode: info.Mode().Perm(), Bytes: len(cur), Digest: proposalContentDigest(cur)}, cur, nil
}
