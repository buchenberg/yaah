package tools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostRmdirTool builds an rmdir tool on the host workspace rooted at root.
func hostRmdirTool(root string) *RmdirTool {
	pv := NewPathValidator(root, false, nil)
	return &RmdirTool{PV: pv, WS: NewLocalWorkspace(pv)}
}

func TestRmdirTool_removesEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "empty")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := hostRmdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": target}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "Removed directory") {
		t.Errorf("result = %q, want a removal confirmation", result)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("directory still present: %v", statErr)
	}
}

func TestRmdirTool_refusesNonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "full")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(target, "keep.txt"), []byte("keep"))

	_, err := hostRmdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": target}))
	if err == nil {
		t.Fatal("removing a non-empty directory must fail")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not empty") {
		t.Errorf("error = %q, want a not-empty refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "keep.txt")); statErr != nil {
		t.Errorf("non-empty directory lost its contents: %v", statErr)
	}
}

func TestRmdirTool_refusesFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "regular.txt")
	writeFile(t, file, []byte("x"))

	_, err := hostRmdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": file}))
	if err == nil {
		t.Fatal("rmdir on a file must be refused")
	}
	if !strings.Contains(err.Error(), "use delete") {
		t.Errorf("error = %q, want a pointer to delete", err)
	}
	if _, statErr := os.Stat(file); statErr != nil {
		t.Errorf("file removed by a refused rmdir: %v", statErr)
	}
}

func TestRmdirTool_recursiveRemovesTree(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tree, "file.txt"), []byte("x"))
	writeFile(t, filepath.Join(tree, "nested", "deep.txt"), []byte("y"))

	result, err := hostRmdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": tree, "recursive": true}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "tree") {
		t.Errorf("result = %q, want the removed path", result)
	}
	if _, statErr := os.Stat(tree); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("tree still present: %v", statErr)
	}
}

func TestRmdirTool_recursiveRefusesFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "regular.txt")
	writeFile(t, file, []byte("x"))

	_, err := hostRmdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": file, "recursive": true}))
	if err == nil {
		t.Fatal("recursive rmdir on a file must be refused")
	}
	if !strings.Contains(err.Error(), "use delete") {
		t.Errorf("error = %q, want a pointer to delete", err)
	}
	if _, statErr := os.Stat(file); statErr != nil {
		t.Errorf("file removed by a refused rmdir: %v", statErr)
	}
}

func TestRmdirTool_refusesWorkspaceRoot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "keep.txt"), []byte("keep"))

	_, err := hostRmdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": dir, "recursive": true}))
	if err == nil {
		t.Fatal("recursive rmdir of the workspace root must be refused")
	}
	if !strings.Contains(err.Error(), "workspace root") {
		t.Errorf("error = %q, want a workspace-root refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "keep.txt")); statErr != nil {
		t.Errorf("workspace root contents were removed: %v", statErr)
	}
}

func TestRmdirTool_missingPath(t *testing.T) {
	dir := t.TempDir()
	_, err := hostRmdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": filepath.Join(dir, "gone")}))
	if err == nil {
		t.Fatal("removing a missing directory must fail")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want fs.ErrNotExist", err)
	}
}

func TestRmdirTool_pathValidatorDenial(t *testing.T) {
	dir := t.TempDir()
	pv := NewPathValidator(dir, false, []string{"*.secret"})
	tool := &RmdirTool{PV: pv, WS: NewLocalWorkspace(pv)}

	if _, err := tool.Execute(context.Background(),
		transferArgs(t, map[string]any{"path": filepath.Join(dir, "a.secret")})); err == nil {
		t.Fatal("a deny pattern must refuse the directory")
	}
	if _, err := tool.Execute(context.Background(),
		transferArgs(t, map[string]any{"path": filepath.Join(dir, "..", "escaped")})); err == nil {
		t.Fatal("a path outside the workspace root must be refused")
	}
}

func TestRmdirTool_sandboxParity(t *testing.T) {
	ws := newSandboxWS(t, nil)
	ctx := context.Background()
	tool := &RmdirTool{WS: ws}

	if err := ws.MkdirAll(ctx, "/workspace/nested/empty", 0o755); err != nil {
		t.Fatalf("seed empty directory: %v", err)
	}
	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace/nested/empty"})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := ws.Stat(ctx, "/workspace/nested/empty"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("empty directory still present in the sandbox: %v", err)
	}

	// A non-empty directory is refused, and its contents stay.
	if err := ws.MkdirAll(ctx, "/workspace/full", 0o755); err != nil {
		t.Fatalf("seed full directory: %v", err)
	}
	if err := ws.WriteFile(ctx, "/workspace/full/keep.txt", []byte("keep"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace/full"})); err == nil {
		t.Fatal("removing a non-empty sandbox directory must fail")
	}
	if _, err := ws.Stat(ctx, "/workspace/full/keep.txt"); err != nil {
		t.Errorf("non-empty sandbox directory lost its contents: %v", err)
	}

	// A file is refused with the same pointer as on the host.
	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace/full/keep.txt"})); err == nil {
		t.Fatal("rmdir on a sandbox file must be refused")
	} else if !strings.Contains(err.Error(), "use delete") {
		t.Errorf("sandbox file error = %q, want a pointer to delete", err)
	}

	// Recursive removal takes the whole tree, and the sandbox root is never a
	// legal target.
	if err := ws.MkdirAll(ctx, "/workspace/tree/nested", 0o755); err != nil {
		t.Fatalf("seed tree: %v", err)
	}
	if err := ws.WriteFile(ctx, "/workspace/tree/nested/deep.txt", []byte("y"), 0o644); err != nil {
		t.Fatalf("seed deep file: %v", err)
	}
	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace/tree", "recursive": true})); err != nil {
		t.Fatalf("recursive Execute: %v", err)
	}
	if _, err := ws.Stat(ctx, "/workspace/tree"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("tree still present in the sandbox: %v", err)
	}
	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace", "recursive": true})); err == nil {
		t.Fatal("recursive rmdir of the sandbox root must be refused")
	}
}

// TestRmdirTool_explicitTreeRemoval pins the intended non-recursive flow: the
// caller removes files, then empty directories deepest first.
func TestRmdirTool_explicitTreeRemoval(t *testing.T) {
	dir := t.TempDir()
	inner := filepath.Join(dir, "tree", "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(inner, "file.txt"), []byte("x"))

	if _, err := hostRmdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": filepath.Join(dir, "tree")})); err == nil {
		t.Fatal("the outer directory is non-empty and must be refused")
	}
	if _, err := (&DeleteTool{WS: NewLocalWorkspace(NewPathValidator(dir, false, nil))}).
		Execute(context.Background(), transferArgs(t, map[string]any{"filePath": filepath.Join(inner, "file.txt")})); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	for _, p := range []string{inner, filepath.Join(dir, "tree")} {
		if _, err := hostRmdirTool(dir).Execute(context.Background(),
			transferArgs(t, map[string]any{"path": p})); err != nil {
			t.Fatalf("rmdir %s: %v", p, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, "tree")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("tree still present: %v", statErr)
	}
}

func TestRmdirTool_isDangerous(t *testing.T) {
	if !(&RmdirTool{}).IsDangerous(`{}`) {
		t.Error("rmdir mutates the workspace and must require approval")
	}
}

func TestRmdirTool_requiresPath(t *testing.T) {
	tool := hostRmdirTool(t.TempDir())
	for _, args := range []string{`{}`, `{"path":""}`, `not json`} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("args %q must be refused", args)
		}
	}
}
