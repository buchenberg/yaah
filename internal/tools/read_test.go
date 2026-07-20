package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTool_readsExistingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	os.WriteFile(path, []byte("hello\ngoodbye\n"), 0o644)

	rt := &ReadTool{}
	args, _ := json.Marshal(map[string]any{"path": path})
	result, err := rt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "hello") {
		t.Errorf("expected content 'hello', got %q", result)
	}
}

func TestReadTool_respectsOffsetAndLimit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "lines.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\nline4\nline5\n"), 0o644)

	rt := &ReadTool{}
	args, _ := json.Marshal(map[string]any{"path": path, "offset": 2, "limit": 2})
	result, err := rt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "line2\nline3" {
		t.Errorf("expected 'line2\\nline3', got %q", result)
	}
}

func TestReadTool_returnsErrorForMissingFile(t *testing.T) {
	rt := &ReadTool{}
	_, err := rt.Execute(context.Background(), `{"path":"/nonexistent/file.txt"}`)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadTool_returnsErrorForEmptyPath(t *testing.T) {
	rt := &ReadTool{}
	_, err := rt.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestReadTool_returnsErrorForInvalidJSON(t *testing.T) {
	rt := &ReadTool{}
	_, err := rt.Execute(context.Background(), `not json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestReadTool_schemaIsValidJSON(t *testing.T) {
	rt := &ReadTool{}
	var v any
	if err := json.Unmarshal(rt.Schema(), &v); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
