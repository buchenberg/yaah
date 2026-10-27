package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTool_readsExistingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	os.WriteFile(path, []byte("hello\ngoodbye\n"), 0o644)

	rt := &ReadTool{}
	args, _ := json.Marshal(map[string]any{"filePath": path})
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
	args, _ := json.Marshal(map[string]any{"filePath": path, "offset": 2, "limit": 2})
	result, err := rt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.HasPrefix(result, "line2\nline3") {
		t.Errorf("expected 'line2\\nline3' prefix, got %q", result)
	}
	if !strings.HasSuffix(result, truncationMarker) {
		t.Errorf("a limit-shortened read must be marked truncated, got %q", result)
	}
}

func TestReadTool_offsetPastEOFIsExplicit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "lines.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\nline4\nline5\n"), 0o644)

	rt := &ReadTool{}
	args, _ := json.Marshal(map[string]any{"filePath": path, "offset": 99})
	_, err := rt.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("an offset past EOF must be an error, not empty content")
	}
	for _, want := range []string{"offset 99", "past end of file", "5 lines"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestReadTool_limitCoveringAllLinesHasNoMarker(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "lines.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\nline4\nline5\n"), 0o644)

	rt := &ReadTool{}
	args, _ := json.Marshal(map[string]any{"filePath": path, "offset": 1, "limit": 5})
	result, err := rt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.Contains(result, truncationMarker) {
		t.Errorf("limit covering the whole file must not be marked truncated: %q", result)
	}
	if result != "line1\nline2\nline3\nline4\nline5" {
		t.Errorf("unexpected content: %q", result)
	}
}

func TestReadTool_emptyFileOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	os.WriteFile(path, nil, 0o644)

	rt := &ReadTool{}
	args, _ := json.Marshal(map[string]any{"filePath": path, "offset": 1})
	result, err := rt.Execute(context.Background(), string(args))
	if err != nil || result != "" {
		t.Errorf("offset 1 on an empty file = (%q, %v), want (\"\", nil)", result, err)
	}

	args, _ = json.Marshal(map[string]any{"filePath": path, "offset": 2})
	if _, err := rt.Execute(context.Background(), string(args)); err == nil {
		t.Error("offset 2 on an empty file must be an error")
	}
}

func TestReadTool_returnsErrorForMissingFile(t *testing.T) {
	rt := &ReadTool{}
	_, err := rt.Execute(context.Background(), `{"filePath":"/nonexistent/file.txt"}`)
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
