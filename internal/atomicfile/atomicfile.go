// Package atomicfile replaces a file atomically, with one behaviour on every
// platform: the caller writes a temp file beside the target, and this package
// moves it into place.
//
// The idea is one line — os.Rename — but the platforms do not agree on what
// that means. POSIX renames over a target that other descriptors still have
// open: the old inode survives until the last reader drops it. Windows has
// mandatory locking, so MoveFileEx(REPLACE_EXISTING) fails with
// ERROR_ACCESS_DENIED while any other handle is open on the target — a
// concurrent reader in the same tool batch, a file watcher, Defender scanning
// the temp file we just wrote. Callers that treat the rename as infallible
// therefore fail on Windows for reasons that have nothing to do with their
// data, which is how a serialized concurrent-edit test came to flake on the
// Windows CI job and nowhere else (yaah-a56f).
//
// The fix is a bounded retry, and it is deliberately Windows-only: on POSIX a
// failure here is real (a missing directory, a cross-device temp file, a
// read-only mount) and retrying it only delays the error.
package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"syscall"
	"time"
)

// Retry schedule for Windows replace contention. The contention this exists
// for is short — a reader finishes and drops its handle — so the whole schedule
// is worse-case ~165 ms, below the threshold at which a user notices a tool
// call stall, and far below any tool timeout.
const (
	replaceAttempts   = 8
	replaceFirstDelay = 3 * time.Millisecond
	replaceMaxDelay   = 40 * time.Millisecond
)

// ErrReplace reports that the temp file could not be moved onto the target.
var ErrReplace = errors.New("atomic replace")

// ReplaceError is a failed replace that deliberately left the temp file in
// place: at that point the temp file is the only copy of the new bytes, and
// removing it would turn a retryable error into data loss. Carrying the path in
// the error is what makes keeping it useful — the operator (or the agent) can
// recover the content instead of redoing the work blind.
type ReplaceError struct {
	// Temp is the temp file that still holds the new content.
	Temp string
	// Err is the underlying rename failure.
	Err error
}

func (e *ReplaceError) Error() string {
	return fmt.Sprintf("atomic replace: %v (the new content is left at %s)", e.Err, e.Temp)
}

// Unwrap exposes the rename failure, so errors.Is(err, fs.ErrPermission) and
// friends keep working through the wrapper.
func (e *ReplaceError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrReplace) work alongside Unwrap.
func (e *ReplaceError) Is(target error) bool { return target == ErrReplace }

// Replace moves tmp onto dst, replacing dst if it exists.
//
// On failure it returns a *ReplaceError naming the temp file, which is left in
// place: it is the only copy of the data at that point, and deleting it here
// would turn a retryable error into data loss. Callers must not remove it
// either — see the contract note in the package doc.
func Replace(tmp, dst string) error {
	if err := replaceWith(os.Rename, policyFor(runtime.GOOS), tmp, dst); err != nil {
		return &ReplaceError{Temp: tmp, Err: err}
	}
	return nil
}

// policy is how often to retry a failed rename, and what counts as worth
// retrying.
type policy struct {
	attempts  int
	first     time.Duration
	max       time.Duration
	retryable func(error) bool
}

// policyFor is the only place the platform is consulted.
func policyFor(goos string) policy {
	if goos != "windows" {
		// One attempt: a rename failure on POSIX is a real error.
		return policy{attempts: 1}
	}
	return policy{
		attempts:  replaceAttempts,
		first:     replaceFirstDelay,
		max:       replaceMaxDelay,
		retryable: IsReplaceContention,
	}
}

// replaceWith runs the retry schedule against an injected rename function, so
// the mechanism can be tested on every platform rather than only where the
// contention exists.
func replaceWith(rename func(oldpath, newpath string) error, p policy, tmp, dst string) error {
	err := rename(tmp, dst)
	if err == nil || p.attempts <= 1 {
		return err
	}
	delay := p.first
	for attempt := 1; attempt < p.attempts; attempt++ {
		if p.retryable == nil || !p.retryable(err) {
			return err
		}
		time.Sleep(delay)
		if err = rename(tmp, dst); err == nil {
			return nil
		}
		if delay *= 2; delay > p.max {
			delay = p.max
		}
	}
	return err
}

// IsReplaceContention reports whether err is the Windows refusal to replace a
// file that another handle still holds open.
//
// It keys off the two numbers Windows actually returns rather than a message
// string: ERROR_ACCESS_DENIED (5) arrives through fs.ErrPermission because that
// is how Go maps it, and ERROR_SHARING_VIOLATION (32) and ERROR_LOCK_VIOLATION
// (33) arrive as bare syscall errnos. The constants are written out because the
// syscall package only defines them on Windows, and this predicate has to
// compile — and be testable — everywhere.
func IsReplaceContention(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		const (
			errorSharingViolation = 32
			errorLockViolation    = 33
		)
		return errno == errorSharingViolation || errno == errorLockViolation
	}
	return false
}
