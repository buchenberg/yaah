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

// TestWriteTool_preservesNonASCII pins the write path to bytes. A sub-agent
// reported a literal NBSP (U+00A0) being degraded to a space on read-back; JSON
// decoding and the atomic write both preserve the bytes, so this test makes the
// guarantee explicit rather than leaving it to inspection.
func TestWriteTool_preservesNonASCII(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "unicode.txt")
	content := "a\u00a0b\ncaf\u00e9 \u00f6 \U0001F600\n"

	args, _ := json.Marshal(map[string]any{"content": content, "filePath": path})
	if _, err := (&WriteTool{}).Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != content {
		t.Fatalf("write changed the bytes:\n got %q\nwant %q", string(data), content)
	}

	readArgs, _ := json.Marshal(map[string]any{"filePath": path})
	out, err := (&ReadTool{}).Execute(context.Background(), string(readArgs))
	if err != nil {
		t.Fatalf("read tool: %v", err)
	}
	if out != content {
		t.Errorf("read tool changed the content:\n got %q\nwant %q", out, content)
	}
}

func TestWriteTool_supervisionMetaRecordsExistence(t *testing.T) {
	tmp := t.TempDir()
	existing := filepath.Join(tmp, "existing.txt")
	os.WriteFile(existing, []byte("old"), 0o644)
	fresh := filepath.Join(tmp, "fresh.txt")

	wt := &WriteTool{}

	meta := map[string]any(nil)
	ctx := WithSupervisionMeta(context.Background(), &meta)
	args, _ := json.Marshal(map[string]any{"content": "next", "filePath": existing})
	if _, err := wt.Execute(ctx, string(args)); err != nil {
		t.Fatalf("existing-file Execute() error: %v", err)
	}
	if meta["file_existed"] != true {
		t.Errorf("file_existed = %v, want true for an overwrite", meta["file_existed"])
	}

	meta = map[string]any(nil)
	ctx = WithSupervisionMeta(context.Background(), &meta)
	args, _ = json.Marshal(map[string]any{"content": "first", "filePath": fresh})
	if _, err := wt.Execute(ctx, string(args)); err != nil {
		t.Fatalf("new-file Execute() error: %v", err)
	}
	if meta["file_existed"] != false {
		t.Errorf("file_existed = %v, want false for first-time creation", meta["file_existed"])
	}

	// No sink: direct tool use outside a supervised loop must not panic.
	args, _ = json.Marshal(map[string]any{"content": "x", "filePath": filepath.Join(tmp, "sinkless.txt")})
	if _, err := wt.Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("sinkless Execute() error: %v", err)
	}
}
