package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGlobTool_findsMatchingFiles(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "a.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(tmp, "b.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(tmp, "c.txt"), []byte("x"), 0o644)

	gt := &GlobTool{}
	args, _ := json.Marshal(map[string]any{"pattern": "*.go", "path": tmp})
	result, err := gt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "a.go") || !contains(result, "b.go") {
		t.Errorf("expected a.go and b.go, got %q", result)
	}
	if contains(result, "c.txt") {
		t.Errorf("should not contain c.txt, got %q", result)
	}
}

func TestGlobTool_noMatches(t *testing.T) {
	tmp := t.TempDir()
	gt := &GlobTool{}
	args, _ := json.Marshal(map[string]any{"pattern": "*.nonexistent", "path": tmp})
	result, err := gt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "No files found." {
		t.Errorf("expected 'No files found.', got %q", result)
	}
}

func TestGlobTool_returnsErrorForEmptyPattern(t *testing.T) {
	gt := &GlobTool{}
	_, err := gt.Execute(context.Background(), `{"path":"."}`)
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}
