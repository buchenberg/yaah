package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestDeleteTool_isDangerous(t *testing.T) {
	dt := &DeleteTool{}
	if !dt.IsDangerous(`{}`) {
		t.Error("DeleteTool should be dangerous")
	}
}
