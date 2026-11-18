package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeRename fails per the script and records every call.
type fakeRename struct {
	errs  []error
	calls int
}

func (f *fakeRename) rename(string, string) error {
	i := f.calls
	f.calls++
	if i < len(f.errs) {
		return f.errs[i]
	}
	return nil
}

// testPolicy keeps the schedule fast enough for tests while exercising the same
// code path production uses.
func testPolicy(attempts int, retryable func(error) bool) policy {
	return policy{attempts: attempts, first: time.Millisecond, max: time.Millisecond, retryable: retryable}
}

func permissionErr() error {
	return &os.LinkError{Op: "rename", Old: "tmp", New: "dst", Err: fs.ErrPermission}
}

func TestReplaceWith(t *testing.T) {
	t.Run("no contention renames once", func(t *testing.T) {
		r := &fakeRename{}
		if err := replaceWith(r.rename, testPolicy(4, IsReplaceContention), "tmp", "dst"); err != nil {
			t.Fatalf("Replace: %v", err)
		}
		if r.calls != 1 {
			t.Errorf("rename calls = %d, want 1", r.calls)
		}
	})

	t.Run("contention is retried until it clears", func(t *testing.T) {
		r := &fakeRename{errs: []error{permissionErr(), permissionErr()}}
		if err := replaceWith(r.rename, testPolicy(4, IsReplaceContention), "tmp", "dst"); err != nil {
			t.Fatalf("Replace: %v", err)
		}
		if r.calls != 3 {
			t.Errorf("rename calls = %d, want 3 (two denials then success)", r.calls)
		}
	})

	t.Run("a real error is not retried", func(t *testing.T) {
		boom := &os.LinkError{Op: "rename", Old: "tmp", New: "dst", Err: fs.ErrNotExist}
		r := &fakeRename{errs: []error{boom}}
		err := replaceWith(r.rename, testPolicy(4, IsReplaceContention), "tmp", "dst")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error = %v, want the original failure", err)
		}
		if r.calls != 1 {
			t.Errorf("rename calls = %d, want 1 (a POSIX-style failure must not be retried)", r.calls)
		}
	})

	t.Run("exhaustion returns the last failure", func(t *testing.T) {
		r := &fakeRename{errs: []error{permissionErr(), permissionErr(), permissionErr(), permissionErr()}}
		err := replaceWith(r.rename, testPolicy(3, IsReplaceContention), "tmp", "dst")
		if !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("error = %v, want the permission failure", err)
		}
		if r.calls != 3 {
			t.Errorf("rename calls = %d, want 3 (the attempt budget)", r.calls)
		}
	})

	t.Run("a single-attempt policy never sleeps", func(t *testing.T) {
		r := &fakeRename{errs: []error{permissionErr()}}
		start := time.Now()
		err := replaceWith(r.rename, policyFor("linux"), "tmp", "dst")
		if err == nil {
			t.Fatal("want an error")
		}
		if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
			t.Errorf("elapsed = %v; the non-Windows policy must not retry", elapsed)
		}
		if r.calls != 1 {
			t.Errorf("rename calls = %d, want 1", r.calls)
		}
	})
}

// TestPolicyFor pins the platform split, which is the part users depend on: a
// POSIX rename failure is a real error and must surface immediately.
func TestPolicyFor(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		if p := policyFor(goos); p.attempts != 1 {
			t.Errorf("policyFor(%q).attempts = %d, want 1", goos, p.attempts)
		}
	}
	p := policyFor("windows")
	if p.attempts != replaceAttempts {
		t.Errorf("policyFor(windows).attempts = %d, want %d", p.attempts, replaceAttempts)
	}
	if p.retryable == nil {
		t.Error("the Windows policy has no retryable predicate")
	}
	// The schedule must stay well under a tool-call stall.
	var total time.Duration
	delay := p.first
	for i := 1; i < p.attempts; i++ {
		total += delay
		if delay *= 2; delay > p.max {
			delay = p.max
		}
	}
	if total > 500*time.Millisecond {
		t.Errorf("worst-case retry schedule = %v, want <= 500ms", total)
	}
}

// TestIsReplaceContention pins the classification. The numbers are what Windows
// reports; on other platforms the same values can only be constructed, which is
// exactly what lets this run in every CI job.
func TestIsReplaceContention(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"access denied as permission", permissionErr(), true},
		{"sharing violation", &os.LinkError{Op: "rename", Old: "tmp", New: "dst", Err: syscall.Errno(32)}, true},
		{"lock violation", &os.LinkError{Op: "rename", Old: "tmp", New: "dst", Err: syscall.Errno(33)}, true},
		{"missing file", &os.LinkError{Op: "rename", Old: "tmp", New: "dst", Err: fs.ErrNotExist}, false},
		{"unrelated errno", &os.LinkError{Op: "rename", Old: "tmp", New: "dst", Err: syscall.Errno(2)}, false},
		{"nil", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsReplaceContention(tc.err); got != tc.want {
				t.Errorf("IsReplaceContention(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestReplaceWrapsFailure pins the sentinel and the retained path: a caller
// must be able to tell an atomic replace failure from any other file error and
// say where the new bytes still are.
func TestReplaceWrapsFailure(t *testing.T) {
	dir := t.TempDir()
	tmp := dir + string(os.PathSeparator) + "missing-tmp"
	err := Replace(tmp, dir+string(os.PathSeparator)+"dst")
	if err == nil {
		t.Fatal("want an error for a missing temp file")
	}
	if !errors.Is(err, ErrReplace) {
		t.Errorf("error = %v, want it to report ErrReplace", err)
	}
	var re *ReplaceError
	if !errors.As(err, &re) {
		t.Fatalf("error = %v (%T), want a *ReplaceError", err, err)
	}
	if re.Temp != tmp {
		t.Errorf("ReplaceError.Temp = %q, want %q", re.Temp, tmp)
	}
	if !strings.Contains(err.Error(), "atomic replace") || !strings.Contains(err.Error(), tmp) {
		t.Errorf("error = %v, want the sentinel text and the retained path", err)
	}
}
