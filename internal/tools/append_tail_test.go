package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appendTailWorkspace(t *testing.T) (string, Workspace) {
	t.Helper()
	dir := t.TempDir()
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	return pv.WorkspaceRoot, newLocalWorkspace(pv)
}

func TestAppendToolCreatesAndAppends(t *testing.T) {
	dir, ws := appendTailWorkspace(t)
	tool := &AppendTool{WS: ws}
	path := filepath.Join(dir, "ledger.jsonl")

	for _, line := range []string{`{"n":1}` + "\n", `{"n":2}` + "\n"} {
		args := fmt.Sprintf(`{"filePath":%q,"content":%q}`, path, line)
		if _, err := tool.Execute(context.Background(), args); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "{\"n\":1}\n{\"n\":2}\n" {
		t.Fatalf("appended content = %q", data)
	}
	if !tool.IsDangerous("{}") {
		t.Fatal("append must be approval-gated")
	}
}

func TestAppendToolRequiresExistingParent(t *testing.T) {
	dir, ws := appendTailWorkspace(t)
	tool := &AppendTool{WS: ws}
	args := fmt.Sprintf(`{"filePath":%q,"content":"x"}`, filepath.Join(dir, "missing", "log"))
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("append created a missing parent instead of failing")
	}
}

func TestTailToolLinesAndBytes(t *testing.T) {
	dir, ws := appendTailWorkspace(t)
	path := filepath.Join(dir, "big.log")
	var b strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &TailTool{WS: ws}

	args := fmt.Sprintf(`{"filePath":%q,"lines":3}`, path)
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("lines: %v", err)
	}
	if out != "...[earlier lines omitted]...\nline 998\nline 999\nline 1000" {
		t.Fatalf("lines out = %q", out)
	}

	args = fmt.Sprintf(`{"filePath":%q,"bytes":4}`, path)
	out, err = tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("bytes: %v", err)
	}
	if out != "000\n" {
		// The file ends with "line 1000\n".
		t.Fatalf("last 4 bytes = %q", out)
	}
}

func TestTailToolByteWindowAndNoTrailingNewline(t *testing.T) {
	dir, ws := appendTailWorkspace(t)
	path := filepath.Join(dir, "window.txt")
	if err := os.WriteFile(path, []byte("abcdefghij"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &TailTool{WS: ws}

	out, err := tool.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"bytes":3,"offset":2}`, path))
	if err != nil {
		t.Fatalf("bytes+offset: %v", err)
	}
	if out != "fgh" {
		t.Fatalf("bytes+offset out = %q, want fgh", out)
	}

	out, err = tool.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"lines":2}`, path))
	if err != nil {
		t.Fatalf("lines: %v", err)
	}
	if out != "abcdefghij" {
		t.Fatalf("single-line tail = %q", out)
	}
	if _, err := tool.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"offset":2}`, path)); err == nil {
		t.Fatal("tail accepted offset without bytes")
	}
}

func TestTailToolDirectory(t *testing.T) {
	dir, ws := appendTailWorkspace(t)
	tool := &TailTool{WS: ws}
	if _, err := tool.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q}`, dir)); err == nil {
		t.Fatal("tail read a directory")
	}
}
