package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLsTool_listsDirectoryContents(t *testing.T) {
	tmp := t.TempDir()
	os.Mkdir(filepath.Join(tmp, "subdir"), 0o755)
	os.WriteFile(filepath.Join(tmp, "file1.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(tmp, "file2.go"), []byte("x"), 0o644)

	lt := &LsTool{}
	args, _ := json.Marshal(map[string]any{"path": tmp})
	result, err := lt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "file1.txt") || !contains(result, "file2.go") {
		t.Errorf("expected file listings, got %q", result)
	}
	if !contains(result, "subdir/") {
		t.Errorf("expected subdir/ with trailing slash, got %q", result)
	}
}

func TestLsTool_respectsDepth(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "d1", "d2"), 0o755)
	os.WriteFile(filepath.Join(tmp, "d1", "d2", "deep.txt"), []byte("x"), 0o644)

	lt := &LsTool{}
	args, _ := json.Marshal(map[string]any{"path": tmp, "depth": 1})
	result, err := lt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if contains(result, "deep.txt") {
		t.Errorf("depth 1 should not show nested files, got %q", result)
	}
}

func TestLsTool_emptyDirectory(t *testing.T) {
	tmp := t.TempDir()
	lt := &LsTool{}
	args, _ := json.Marshal(map[string]any{"path": tmp})
	result, err := lt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "(empty directory)" && result == "" {
		t.Errorf("expected '(empty directory)' or non-empty, got %q", result)
	}
}
