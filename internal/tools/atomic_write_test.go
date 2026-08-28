package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// requirePOSIXPerms skips on platforms (Windows) that do not honor
// Unix permission bits.
func requirePOSIXPerms(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not honor Unix permission bits")
	}
}

// TestAtomicWriteFile_preservesMode pins the mode-preservation rule:
// an existing file's permission bits survive the rewrite (plan 7.5).
func TestAtomicWriteFile_preservesMode(t *testing.T) {
	requirePOSIXPerms(t)
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(path, []byte("rewritten"), 0o644); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600 (existing mode preserved)", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "rewritten" {
		t.Errorf("content = %q, want rewritten", data)
	}
}

// TestAtomicWriteFile_newFileUsesPerm pins the perm for newly created
// files.
func TestAtomicWriteFile_newFileUsesPerm(t *testing.T) {
	requirePOSIXPerms(t)
	path := filepath.Join(t.TempDir(), "new.txt")
	if err := atomicWriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

// TestAtomicWriteFile_leavesNoTempFiles pins the temp cleanup: a crash
// free run must not litter the target directory with dot-temp files.
func TestAtomicWriteFile_leavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	for i := 0; i < 5; i++ {
		if err := atomicWriteFile(path, []byte("content"), 0o644); err != nil {
			t.Fatalf("atomicWriteFile: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp artifact left behind: %s", e.Name())
		}
	}
}
