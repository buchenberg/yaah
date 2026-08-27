package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEditTool_singleReplace(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	os.WriteFile(path, []byte("hello world\nfoo bar\n"), 0o644)

	et := &EditTool{}
	args, _ := json.Marshal(map[string]any{
		"filePath":  path,
		"oldString": "hello",
		"newString": "goodbye",
	})
	_, err := et.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !contains(string(data), "goodbye") {
		t.Errorf("expected 'goodbye' in file, got %q", string(data))
	}
}

func TestEditTool_replaceAll(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	os.WriteFile(path, []byte("cat cat cat\n"), 0o644)

	et := &EditTool{}
	args, _ := json.Marshal(map[string]any{
		"filePath":   path,
		"oldString":  "cat",
		"newString":  "dog",
		"replaceAll": true,
	})
	_, err := et.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "dog dog dog\n" {
		t.Errorf("expected 'dog dog dog\\n', got %q", string(data))
	}
}

func TestEditTool_errorsOnMultipleMatchesWithoutReplaceAll(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	os.WriteFile(path, []byte("cat cat cat\n"), 0o644)

	et := &EditTool{}
	args, _ := json.Marshal(map[string]any{
		"filePath":  path,
		"oldString": "cat",
		"newString": "dog",
	})
	_, err := et.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected error for multiple matches without replaceAll")
	}
}

func TestEditTool_errorsWhenOldStringNotFound(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	os.WriteFile(path, []byte("hello world\n"), 0o644)

	et := &EditTool{}
	args, _ := json.Marshal(map[string]any{
		"filePath":  path,
		"oldString": "nonexistent",
		"newString": "replacement",
	})
	_, err := et.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected error when oldString not found")
	}
}

func TestEditTool_multiEdit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644)

	et := &EditTool{}
	args, _ := json.Marshal(map[string]any{
		"filePath": path,
		"edits": []map[string]string{
			{"oldString": "alpha", "newString": "ALPHA"},
			{"oldString": "beta", "newString": "BETA"},
		},
	})
	_, err := et.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !contains(string(data), "ALPHA") || !contains(string(data), "BETA") {
		t.Errorf("expected ALPHA and BETA in file, got %q", string(data))
	}
}

func TestEditTool_errorsForSameOldAndNew(t *testing.T) {
	et := &EditTool{}
	_, err := et.Execute(context.Background(), `{"filePath":"x","oldString":"a","newString":"a"}`)
	if err == nil {
		t.Fatal("expected error when oldString == newString")
	}
}

func TestEditTool_isDangerous(t *testing.T) {
	et := &EditTool{}
	if !et.IsDangerous(`{}`) {
		t.Error("EditTool should be dangerous")
	}
}

func TestEditTool_fuzzyTabNormalized(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.go")

	// Write a Go source file with tab indentation (common in Go).
	fileContent := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n\tfmt.Println(\"world\")\n}\n"
	os.WriteFile(path, []byte(fileContent), 0o644)

	et := &EditTool{}

	// Model reads the file with Read tool (preserves tabs), but when it
	// constructs oldString in JSON, it might use spaces instead of tabs.
	// This is the common failure mode: oldString has 4-space indentation
	// but the file has tabs.
	args, _ := json.Marshal(map[string]any{
		"filePath":  path,
		"oldString": "    fmt.Println(\"hello\")",
		"newString": "    fmt.Println(\"HELLO\")",
	})
	_, err := et.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("tab-normalized fuzzy match failed: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !contains(string(data), "HELLO") {
		t.Errorf("expected HELLO in file, got %q", string(data))
	}
}

func TestEditTool_fuzzyMultiLineTabNormalized(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.go")

	// Multi-line Go code block with tabs
	fileContent := "func foo() {\n\tx := 1\n\tif x > 0 {\n\t\tbar()\n\t}\n}\n"
	os.WriteFile(path, []byte(fileContent), 0o644)

	et := &EditTool{}

	// Model sends oldString with spaces instead of tabs for all lines
	args, _ := json.Marshal(map[string]any{
		"filePath":  path,
		"oldString": "    x := 1\n    if x > 0 {\n        bar()\n    }",
		"newString": "    x := 2\n    if x > 0 {\n        baz()\n    }",
	})
	_, err := et.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("multi-line tab-normalized fuzzy match failed: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !contains(string(data), "baz()") {
		t.Errorf("expected baz() in file, got %q", string(data))
	}
}

// TestEditTool_fuzzyFailsWhenMappedMatchNotUnique verifies that when
// exact match finds multiple matches, it errors before trying fuzzy match.
func TestEditTool_fuzzyFailsWhenMappedMatchNotUnique(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")

	// File has two similar regions where exact match finds both
	fileContent := "func foo() {\n    x := 1\n}\n\nfunc bar() {\n    x := 1 \n}\n"
	os.WriteFile(path, []byte(fileContent), 0o644)

	et := &EditTool{}

	// oldString with no trailing space - matches both exactly
	args, _ := json.Marshal(map[string]any{
		"filePath":  path,
		"oldString": "    x := 1",
		"newString": "    x := 2",
	})
	_, err := et.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected error when exact match finds multiple")
	}
	if !contains(err.Error(), "found 2 matches") {
		t.Errorf("expected multiple matches error, got %q", err.Error())
	}
}

// TestEditTool_fuzzyFailsWhenMappedMatchNotFound verifies that fuzzy
// matching fails when the mapped-back candidate doesn't actually exist
// in the original content (contentBytePos returned wrong location).
func TestEditTool_fuzzyFailsWhenMappedMatchNotFound(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")

	// File content where normalization creates a false positive match
	// that doesn't map back to a real occurrence
	fileContent := "alpha\nbeta\ngamma\n"
	os.WriteFile(path, []byte(fileContent), 0o644)

	et := &EditTool{}

	// oldString that's close but not exact - normalization might find
	// a match but the byte position mapping fails
	args, _ := json.Marshal(map[string]any{
		"filePath":  path,
		"oldString": "alp ha", // close to "alpha" but not exact
		"newString": "ALPHA",
	})
	_, err := et.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected error when fuzzy match maps to non-existent location")
	}
	if !contains(err.Error(), "oldString not found") {
		t.Errorf("expected oldString not found error, got %q", err.Error())
	}
}
