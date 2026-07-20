package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteTool_writesNewFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "out.txt")

	wt := &WriteTool{}
	args, _ := json.Marshal(map[string]any{"content": "hello world", "filePath": path})
	result, err := wt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello world" {
		t.Errorf("file content = %q, want %q", string(data), "hello world")
	}
	if !strings.Contains(result, "Wrote") {
		t.Errorf("result should mention bytes written, got %q", result)
	}
}

func TestWriteTool_overwritesExistingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "existing.txt")
	os.WriteFile(path, []byte("old content"), 0o644)

	wt := &WriteTool{}
	args, _ := json.Marshal(map[string]any{"content": "new content", "filePath": path})
	_, err := wt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "new content" {
		t.Errorf("file content = %q, want %q", string(data), "new content")
	}
}

func TestWriteTool_returnsErrorForMissingPath(t *testing.T) {
	wt := &WriteTool{}
	_, err := wt.Execute(context.Background(), `{"content":"hi"}`)
	if err == nil {
		t.Fatal("expected error for missing filePath")
	}
}

func TestWriteTool_returnsErrorForInvalidJSON(t *testing.T) {
	wt := &WriteTool{}
	_, err := wt.Execute(context.Background(), `bad json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestWriteTool_isDangerous(t *testing.T) {
	wt := &WriteTool{}
	if !wt.IsDangerous(`{}`) {
		t.Error("WriteTool should be dangerous")
	}
}
