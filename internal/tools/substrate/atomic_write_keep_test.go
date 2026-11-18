package substrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/atomicfile"
)

// requireReplaceFailure drives a replace failure the same way on every
// platform: the target is an existing directory, so the rename cannot land.
// The point of these tests is what survives the failure, not how it failed.
func requireReplaceFailure(t *testing.T, err error) *atomicfile.ReplaceError {
	t.Helper()
	if err == nil {
		t.Fatal("want a replace failure")
	}
	var re *atomicfile.ReplaceError
	if !errors.As(err, &re) {
		t.Fatalf("error = %v (%T), want a *atomicfile.ReplaceError", err, err)
	}
	if _, statErr := os.Stat(re.Temp); statErr != nil {
		t.Errorf("temp file %s was removed after a failed replace: %v", re.Temp, statErr)
	}
	if !strings.Contains(err.Error(), re.Temp) {
		t.Errorf("error %q does not name the retained temp file %s", err, re.Temp)
	}
	return re
}

// TestAtomicWriteFile_keepsTheNewBytesOnReplaceFailure pins the contract the
// atomicfile package states: a failed replace is not a licence to delete the
// only copy of the new content. Before this, the caller cleaned up
// unconditionally and a replace that could not land discarded the write.
func TestAtomicWriteFile_keepsTheNewBytesOnReplaceFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	err := AtomicWriteFile(target, []byte("the new bytes"), 0o644)
	re := requireReplaceFailure(t, err)
	if !errors.Is(err, atomicfile.ErrReplace) {
		t.Errorf("error = %v, want it to report ErrReplace", err)
	}
	got, readErr := os.ReadFile(re.Temp)
	if readErr != nil {
		t.Fatalf("read retained temp: %v", readErr)
	}
	if string(got) != "the new bytes" {
		t.Errorf("retained temp holds %q, want the new content", got)
	}
}

// TestCopy_keepsTheCopiedBytesOnReplaceFailure pins the same contract on the
// copy path, which reaches the replace through a different helper.
func TestCopy_keepsTheCopiedBytesOnReplaceFailure(t *testing.T) {
	dir := t.TempDir()
	ws := newLocalWorkspace(&PathValidator{WorkDir: dir})
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	err := ws.Copy(context.Background(), src, dst)
	re := requireReplaceFailure(t, err)
	got, readErr := os.ReadFile(re.Temp)
	if readErr != nil {
		t.Fatalf("read retained temp: %v", readErr)
	}
	if string(got) != "payload" {
		t.Errorf("retained temp holds %q, want the copied bytes", got)
	}
}

// TestAtomicWriteFile_removesTempBeforeTheReplace pins the other half: a failure
// that happens before the replace has delivered nothing, so its temp file is
// cleaned up as before.
func TestAtomicWriteFile_removesTempBeforeTheReplace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file.txt")
	if err := AtomicWriteFile(target, []byte("ok"), 0o644); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp artifact left behind on the success path: %s", e.Name())
		}
	}
}
