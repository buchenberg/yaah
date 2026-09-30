package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCollectFilesSkipsSymlinkRoot pins the direct-file fast path on Lstat: a
// root that is a symlink to a regular file yields no files instead of handing
// sed a path outside the lexical workspace, while a regular-file root keeps
// the fast path.
func TestCollectFilesSkipsSymlinkRoot(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	ws := newLocalWorkspace(NewPathValidator(dir, false, nil))
	files, err := collectFiles(context.Background(), ws, link, "")
	if err != nil {
		t.Fatalf("collectFiles(symlink root): %v", err)
	}
	if len(files) != 0 {
		t.Errorf("symlink root must be skipped, got %v", files)
	}

	files, err = collectFiles(context.Background(), ws, target, "")
	if err != nil {
		t.Fatalf("collectFiles(file root): %v", err)
	}
	if len(files) != 1 || files[0] != target {
		t.Errorf("regular-file root = %v, want [%s]", files, target)
	}
}
