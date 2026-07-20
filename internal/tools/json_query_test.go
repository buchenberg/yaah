package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeJSONFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.WriteFile(p, []byte(content), 0o644)
	return p
}

func TestJSONQuery_readTopLevelKey(t *testing.T) {
	tmp := t.TempDir()
	f := writeJSONFile(t, tmp, "cfg.json", `{"name":"test","version":"1.0"}`)

	jq := &JSONQueryTool{}
	args, _ := json.Marshal(map[string]any{"file": f, "path": "name"})
	result, err := jq.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, `"test"`) {
		t.Errorf("expected 'test' in result, got %q", result)
	}
}

func TestJSONQuery_readNestedKey(t *testing.T) {
	tmp := t.TempDir()
	f := writeJSONFile(t, tmp, "cfg.json", `{"dependencies":{"react":"18.0.0"}}`)

	jq := &JSONQueryTool{}
	args, _ := json.Marshal(map[string]any{"file": f, "path": "dependencies.react"})
	result, err := jq.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, `"18.0.0"`) {
		t.Errorf("expected '18.0.0' in result, got %q", result)
	}
}

func TestJSONQuery_readWholeFile(t *testing.T) {
	tmp := t.TempDir()
	f := writeJSONFile(t, tmp, "cfg.json", `{"a":1,"b":2}`)

	jq := &JSONQueryTool{}
	args, _ := json.Marshal(map[string]any{"file": f})
	result, err := jq.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, `"a": 1`) || !contains(result, `"b": 2`) {
		t.Errorf("expected full object, got %q", result)
	}
}

func TestJSONQuery_writeValue(t *testing.T) {
	tmp := t.TempDir()
	f := writeJSONFile(t, tmp, "cfg.json", `{"react":"17.0.0"}`)

	jq := &JSONQueryTool{}
	args, _ := json.Marshal(map[string]any{
		"file": f,
		"path": "react",
		"set":  `"19.0.0"`,
	})
	result, err := jq.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "Set react") {
		t.Errorf("expected set confirmation, got %q", result)
	}

	data, _ := os.ReadFile(f)
	if !contains(string(data), "19.0.0") {
		t.Errorf("expected '19.0.0' in file, got %q", string(data))
	}
}

func TestJSONQuery_deleteKey(t *testing.T) {
	tmp := t.TempDir()
	f := writeJSONFile(t, tmp, "cfg.json", `{"keep":"yes","remove":"no"}`)

	jq := &JSONQueryTool{}
	args, _ := json.Marshal(map[string]any{
		"file":   f,
		"path":   "remove",
		"action": "delete",
	})
	result, err := jq.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "Deleted remove") {
		t.Errorf("expected delete confirmation, got %q", result)
	}

	data, _ := os.ReadFile(f)
	if contains(string(data), "remove") {
		t.Error("key 'remove' should be deleted")
	}
	if !contains(string(data), "keep") {
		t.Error("key 'keep' should remain")
	}
}

func TestJSONQuery_readMissingKey(t *testing.T) {
	tmp := t.TempDir()
	f := writeJSONFile(t, tmp, "cfg.json", `{"name":"test"}`)

	jq := &JSONQueryTool{}
	args, _ := json.Marshal(map[string]any{"file": f, "path": "nonexistent"})
	_, err := jq.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestJSONQuery_invalidJSON(t *testing.T) {
	tmp := t.TempDir()
	f := writeJSONFile(t, tmp, "bad.json", `not json`)

	jq := &JSONQueryTool{}
	args, _ := json.Marshal(map[string]any{"file": f, "path": "x"})
	_, err := jq.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestJSONQuery_missingFile(t *testing.T) {
	jq := &JSONQueryTool{}
	_, err := jq.Execute(context.Background(), `{"file":"/nonexistent/json.json","path":"x"}`)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestJSONQuery_isDangerousForWrite(t *testing.T) {
	jq := &JSONQueryTool{}
	if jq.IsDangerous(`{}`) {
		t.Error("read should not be dangerous")
	}
	if !jq.IsDangerous(`{"set":"1"}`) {
		t.Error("write should be dangerous")
	}
	if !jq.IsDangerous(`{"action":"write"}`) {
		t.Error("explicit write should be dangerous")
	}
	if !jq.IsDangerous(`{"action":"delete"}`) {
		t.Error("delete should be dangerous")
	}
}

func TestJSONQuery_arrayIndex(t *testing.T) {
	tmp := t.TempDir()
	f := writeJSONFile(t, tmp, "cfg.json", `{"items":["alpha","beta","gamma"]}`)

	jq := &JSONQueryTool{}
	args, _ := json.Marshal(map[string]any{"file": f, "path": "items[1]"})
	result, err := jq.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, `"beta"`) {
		t.Errorf("expected 'beta', got %q", result)
	}
}
