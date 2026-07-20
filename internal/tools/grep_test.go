package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGrepTool_findsMatches(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "test.txt"), []byte("hello world\nfoo bar\nhello again\n"), 0o644)

	gt := &GrepTool{}
	args, _ := json.Marshal(map[string]any{"pattern": "hello", "path": tmp})
	result, err := gt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "hello") {
		t.Errorf("expected matches containing 'hello', got %q", result)
	}
}

func TestGrepTool_noMatches(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "test.txt"), []byte("hello world\n"), 0o644)

	gt := &GrepTool{}
	args, _ := json.Marshal(map[string]any{"pattern": "zzznotfound", "path": tmp})
	result, err := gt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "No matches found." {
		t.Errorf("expected 'No matches found.', got %q", result)
	}
}

func TestGrepTool_returnsErrorForEmptyPattern(t *testing.T) {
	gt := &GrepTool{}
	_, err := gt.Execute(context.Background(), `{"path":"."}`)
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}
