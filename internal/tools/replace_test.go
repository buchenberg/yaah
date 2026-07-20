package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func tmpFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	return p
}

func TestReplaceTool_singleFile(t *testing.T) {
	tmp := t.TempDir()
	tmpFile(t, tmp, "a.go", "package foo\nvar x = 1\nvar y = 2\n")

	rt := &ReplaceTool{}
	args, _ := json.Marshal(map[string]any{
		"pattern":     `var (\w+) = \d+`,
		"replacement": `let $1 = 0`,
		"path":        tmp,
		"include":     "*.go",
	})
	result, err := rt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "a.go: 2 match(es)") {
		t.Errorf("expected 2 matches in a.go, got %q", result)
	}

	data, _ := os.ReadFile(filepath.Join(tmp, "a.go"))
	if !contains(string(data), "let x = 0") {
		t.Errorf("expected 'let x = 0', got %q", string(data))
	}
}

func TestReplaceTool_dryRun(t *testing.T) {
	tmp := t.TempDir()
	tmpFile(t, tmp, "a.go", "hello world\n")

	rt := &ReplaceTool{}
	args, _ := json.Marshal(map[string]any{
		"pattern":     `hello`,
		"replacement": "goodbye",
		"path":        tmp,
		"include":     "*.go",
		"dry_run":     true,
	})
	result, err := rt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "DRY RUN") {
		t.Errorf("expected DRY RUN marker, got %q", result)
	}

	data, _ := os.ReadFile(filepath.Join(tmp, "a.go"))
	if string(data) != "hello world\n" {
		t.Errorf("dry run should not modify file, got %q", string(data))
	}
}

func TestReplaceTool_multipleFiles(t *testing.T) {
	tmp := t.TempDir()
	tmpFile(t, tmp, "a.go", "const x = 1\n")
	tmpFile(t, tmp, "b.go", "const y = 2\n")
	tmpFile(t, tmp, "c.txt", "const z = 3\n")

	rt := &ReplaceTool{}
	args, _ := json.Marshal(map[string]any{
		"pattern":     `const (\w) = \d`,
		"replacement": `var $1`,
		"path":        tmp,
		"include":     "*.go",
	})
	result, err := rt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "a.go: 1 match") || !contains(result, "b.go: 1 match") {
		t.Errorf("expected 1 match each in a.go and b.go, got %q", result)
	}
	if contains(result, "c.txt") {
		t.Errorf("should not have processed c.txt, got %q", result)
	}
}

func TestReplaceTool_noMatches(t *testing.T) {
	tmp := t.TempDir()
	tmpFile(t, tmp, "a.go", "hello world\n")

	rt := &ReplaceTool{}
	args, _ := json.Marshal(map[string]any{
		"pattern":     `zzznotfound`,
		"replacement": "nope",
		"path":        tmp,
	})
	result, err := rt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "0 match(es)") {
		t.Errorf("expected 0 matches, got %q", result)
	}
}

func TestReplaceTool_returnsErrorForInvalidRegex(t *testing.T) {
	rt := &ReplaceTool{}
	_, err := rt.Execute(context.Background(), `{"pattern":"[unclosed","replacement":"x","path":"."}`)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestReplaceTool_returnsErrorForEmptyPattern(t *testing.T) {
	rt := &ReplaceTool{}
	_, err := rt.Execute(context.Background(), `{"replacement":"x","path":"."}`)
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestReplaceTool_isDangerous(t *testing.T) {
	rt := &ReplaceTool{}
	if !rt.IsDangerous(`{}`) {
		t.Error("ReplaceTool should be dangerous")
	}
}
