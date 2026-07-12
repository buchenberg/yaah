package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTool_readsExistingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	if err := os.WriteFile(path, []byte("hello\ngoodbye\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	rt := &ReadTool{}
	result, err := rt.Execute(context.Background(), `{"path":"`+path+`","offset":0,"limit":10}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if result == "" {
		t.Fatal("expected content, got empty string")
	}
}

func TestReadTool_returnsErrorForMissingFile(t *testing.T) {
	rt := &ReadTool{}
	_, err := rt.Execute(context.Background(), `{"path":"/nonexistent/file.txt"}`)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadTool_schemaIsValidJSON(t *testing.T) {
	rt := &ReadTool{}
	schema := rt.Schema()
	if schema == nil || len(schema) == 0 {
		t.Fatal("schema is empty")
	}
}

func TestBashTool_runsSimpleCommand(t *testing.T) {
	bt := &BashTool{}
	result, err := bt.Execute(context.Background(), `{"command":"echo hello"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "hello\n" {
		t.Errorf("result = %q, want %q", result, "hello\n")
	}
}

func TestBashTool_rejectsDangerousCommands(t *testing.T) {
	bt := &BashTool{}
	for _, cmd := range []string{
		`{"command":"rm -rf /"}`,
		`{"command":"shutdown"}`,
		`{"command":"reboot"}`,
		`{"command":"mkfs"}`,
		`{"command":"dd if=/dev/zero"}`,
	} {
		t.Run(cmd, func(t *testing.T) {
			_, err := bt.Execute(context.Background(), cmd)
			if err == nil {
				t.Errorf("expected error for dangerous command: %s", cmd)
			}
		})
	}
}
