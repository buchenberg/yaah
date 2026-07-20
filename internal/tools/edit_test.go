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
