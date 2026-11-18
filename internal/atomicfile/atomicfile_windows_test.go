package atomicfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// holdWithoutDelete opens path with a share mode that excludes delete, which is
// what makes MoveFileEx(REPLACE_EXISTING) refuse the rename. It is the cheapest
// faithful reproduction of the CI flake: a reader holds the file, the writer
// tries to replace it, Windows says "Access is denied".
func holdWithoutDelete(t *testing.T, path string) syscall.Handle {
	t.Helper()
	utf16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	h, err := syscall.CreateFile(utf16, syscall.GENERIC_READ, syscall.FILE_SHARE_READ,
		nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatalf("CreateFile %s: %v", path, err)
	}
	return h
}

// TestReplaceWaitsOutAnOpenHandle is the reason this package exists: the OS
// refuses the replace while another handle is open, and the retry schedule wins
// once that handle goes away.
func TestReplaceWaitsOutAnOpenHandle(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "target.txt")
	tmp := filepath.Join(dir, "target.tmp")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := holdWithoutDelete(t, dst)
	go func() {
		// Well inside the schedule, and far enough out that the first attempt
		// is certain to hit the denial.
		time.Sleep(20 * time.Millisecond)
		_ = syscall.CloseHandle(h)
	}()

	if err := Replace(tmp, dst); err != nil {
		t.Fatalf("Replace: %v (the retry schedule did not outlast a 20ms handle)", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("target = %q, want %q", got, "new")
	}
}

// TestReplaceGivesUpOnAPersistentHandle pins the other half: the retry is
// bounded, and a handle that never goes away still surfaces the real Windows
// error instead of hanging the tool call.
func TestReplaceGivesUpOnAPersistentHandle(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "target.txt")
	tmp := filepath.Join(dir, "target.tmp")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := holdWithoutDelete(t, dst)
	defer syscall.CloseHandle(h)

	start := time.Now()
	err := Replace(tmp, dst)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want an error while the target is held open")
	}
	if !IsReplaceContention(err) {
		t.Errorf("error = %v, want it classified as replace contention", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("elapsed = %v; the retry schedule must stay bounded", elapsed)
	}
	// The temp file is the only copy of the data at this point, so it must
	// survive a failed replace.
	if _, statErr := os.Stat(tmp); statErr != nil {
		t.Errorf("temp file was removed on failure: %v", statErr)
	}
}
