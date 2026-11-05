package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteTool_deletesExistingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "doomed.txt")
	os.WriteFile(path, []byte("bye"), 0o644)

	dt := &DeleteTool{}
	args, _ := json.Marshal(map[string]any{"filePath": path})
	_, err := dt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestDeleteTool_returnsErrorForMissingFile(t *testing.T) {
	dt := &DeleteTool{}
	_, err := dt.Execute(context.Background(), `{"filePath":"/nonexistent/deleted.txt"}`)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestDeleteTool_returnsErrorForEmptyPath(t *testing.T) {
	dt := &DeleteTool{}
	_, err := dt.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestDeleteTool_refusesDirectory(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "empty-dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	dt := &DeleteTool{}
	args, _ := json.Marshal(map[string]any{"filePath": dir})
	_, err := dt.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("delete on a directory must be refused")
	}
	if !strings.Contains(err.Error(), "rmdir") {
		t.Errorf("error = %q, want a pointer to rmdir", err)
	}
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		t.Errorf("directory removed by a refused delete: %v", statErr)
	}
}

func TestDeleteTool_isDangerous(t *testing.T) {
	dt := &DeleteTool{}
	if !dt.IsDangerous(`{}`) {
		t.Error("DeleteTool should be dangerous")
	}
}
