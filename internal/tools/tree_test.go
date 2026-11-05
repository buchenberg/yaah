package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func treeWorkspace(t *testing.T) (string, Workspace) {
	t.Helper()
	dir := t.TempDir()
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	return pv.WorkspaceRoot, newLocalWorkspace(pv)
}

func seedTree(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(root, "top.txt"):           "top\n",
		filepath.Join(root, "a", "one.txt"):      "one\n",
		filepath.Join(root, "a", "b", "two.txt"): "two\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCopyTree(t *testing.T) {
	dir, ws := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	tool := &CopyTreeTool{WS: ws}
	args := `{"path":"` + src + `","to":"` + filepath.Join(dir, "dst", "nested") + `","mkdir_p":true}`
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out == "" {
		t.Fatal("empty result")
	}
	for path, want := range map[string]string{
		"top.txt":     "top\n",
		"a/one.txt":   "one\n",
		"a/b/two.txt": "two\n",
	} {
		data, err := os.ReadFile(filepath.Join(dir, "dst", "nested", path))
		if err != nil {
			t.Fatalf("copied %s: %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("copied %s = %q, want %q", path, data, want)
		}
	}
}

func TestCopyTreeRefusesSymlink(t *testing.T) {
	dir, ws := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	if err := os.Symlink("top.txt", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool := &CopyTreeTool{WS: ws}
	if _, err := tool.Execute(context.Background(), `{"path":"`+src+`","to":"`+filepath.Join(dir, "dst")+`"}`); err == nil {
		t.Fatal("copy_tree followed a symlink")
	}
}

func TestMoveTree(t *testing.T) {
	dir, ws := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	dst := filepath.Join(dir, "dst")
	tool := &MoveTreeTool{WS: ws}
	out, err := tool.Execute(context.Background(), `{"path":"`+src+`","to":"`+dst+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out == "" {
		t.Fatal("empty result")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists after move: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "a", "b", "two.txt"))
	if err != nil || string(data) != "two\n" {
		t.Fatalf("moved content = %q, %v", data, err)
	}
}

func TestCopyTreeRefusesDestinationInsideSource(t *testing.T) {
	dir, ws := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	tool := &CopyTreeTool{WS: ws}
	if _, err := tool.Execute(context.Background(), `{"path":"`+src+`","to":"`+filepath.Join(src, "copy")+`"}`); err == nil {
		t.Fatal("copy_tree allowed a destination inside its source")
	}
}

func TestCopyToolMkdirP(t *testing.T) {
	dir, ws := treeWorkspace(t)
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &CopyTool{WS: ws}
	dst := filepath.Join(dir, "deep", "nested", "b.txt")
	if _, err := tool.Execute(context.Background(), `{"path":"`+src+`","to":"`+dst+`","mkdir_p":true}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "x" {
		t.Fatalf("copied content = %q, %v", data, err)
	}
}
