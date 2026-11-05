package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	nestedDst := filepath.Join(dir, "dst", "nested")
	args := mustJSON(t, map[string]any{"path": src, "to": nestedDst, "mkdir_p": true})
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
	dst := filepath.Join(dir, "dst")
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": src, "to": dst})); err == nil {
		t.Fatal("copy_tree followed a symlink")
	}
}

func TestMoveTree(t *testing.T) {
	dir, ws := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	dst := filepath.Join(dir, "dst")
	tool := &MoveTreeTool{WS: ws}
	out, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": src, "to": dst}))
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
	inside := filepath.Join(src, "copy")
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": src, "to": inside})); err == nil {
		t.Fatal("copy_tree allowed a destination inside its source")
	}
}

type moveErrorWorkspace struct {
	Workspace
	err error
}

func (w moveErrorWorkspace) Move(context.Context, string, string) error { return w.err }

// partialTreeMoveWorkspace refuses the source rename (cross-device) and fails the
// source removal after the copy, leaving a complete dst and a partial src.
type partialTreeMoveWorkspace struct {
	Workspace
	source     string
	failRemove string
	moveErr    error
}

func (w partialTreeMoveWorkspace) Move(ctx context.Context, src, dst string) error {
	if src == w.source {
		if w.moveErr != nil {
			return w.moveErr
		}
		return fmt.Errorf("%w: simulated", errCrossDevice)
	}
	return w.Workspace.Move(ctx, src, dst)
}

func (w partialTreeMoveWorkspace) RemoveAll(ctx context.Context, p string) error {
	if p == w.failRemove {
		return errors.New("injected remove failure")
	}
	return w.Workspace.RemoveAll(ctx, p)
}

func TestMoveWorkspaceTreeFallsBackOnlyForCrossDevice(t *testing.T) {
	dir, base := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	dst := filepath.Join(dir, "dst")
	ws := moveErrorWorkspace{Workspace: base, err: syscall.EXDEV}
	stats, err := moveWorkspaceTree(context.Background(), ws, src, dst)
	if err != nil {
		t.Fatalf("cross-device fallback: %v", err)
	}
	if stats.Files != 3 {
		t.Fatalf("cross-device stats = %+v", stats)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("cross-device fallback left the source: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dst, "a", "b", "two.txt")); err != nil || string(data) != "two\n" {
		t.Fatalf("cross-device fallback content = %q, %v", data, err)
	}

	srcSentinel := filepath.Join(dir, "src-sentinel")
	seedTree(t, srcSentinel)
	dstSentinel := filepath.Join(dir, "dst-sentinel")
	sentinelWS := moveErrorWorkspace{
		Workspace: base,
		err:       fmt.Errorf("%w: mv: invalid cross-device link", errCrossDevice),
	}
	if _, err := moveWorkspaceTree(context.Background(), sentinelWS, srcSentinel, dstSentinel); err != nil {
		t.Fatalf("substrate cross-device fallback: %v", err)
	}
	if _, err := os.Stat(dstSentinel); err != nil {
		t.Fatalf("substrate cross-device destination: %v", err)
	}

	src2 := filepath.Join(dir, "src2")
	seedTree(t, src2)
	dst2 := filepath.Join(dir, "dst2")
	ws2 := moveErrorWorkspace{Workspace: base, err: errors.New("permission denied")}
	if _, err := moveWorkspaceTree(context.Background(), ws2, src2, dst2); err == nil {
		t.Fatal("non-cross-device rename error silently fell back to copy")
	}
	if _, err := os.Stat(src2); err != nil {
		t.Fatalf("non-cross-device failure removed the source: %v", err)
	}
	if _, err := os.Stat(dst2); !os.IsNotExist(err) {
		t.Fatalf("non-cross-device failure created the destination: %v", err)
	}
}

func TestMoveTreeForceKeepsNewDestinationWhenSourceRemovalFails(t *testing.T) {
	dir, base := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := partialTreeMoveWorkspace{Workspace: base, source: src, failRemove: src}
	_, err := replaceTreeMove(context.Background(), ws, src, dst)
	if err == nil {
		t.Fatal("partial source removal reported success")
	}
	if !strings.Contains(err.Error(), "new destination kept") {
		t.Fatalf("error does not preserve the new destination: %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(dst, "top.txt"))
	if readErr != nil || string(data) != "top\n" {
		t.Fatalf("new destination was discarded: %q, %v", data, readErr)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var backup string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".yaah-tmp-") {
			backup = filepath.Join(dir, entry.Name())
			break
		}
	}
	if backup == "" {
		t.Fatal("old destination backup was not kept")
	}
	old, readErr := os.ReadFile(filepath.Join(backup, "old.txt"))
	if readErr != nil || string(old) != "old" {
		t.Fatalf("backup content = %q, %v", old, readErr)
	}
}

func TestMoveTreeForceRestoresBackupWhenCopyFails(t *testing.T) {
	dir, base := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	if err := os.Symlink("top.txt", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := partialTreeMoveWorkspace{
		Workspace: base,
		source:    src,
		moveErr:   fmt.Errorf("%w: simulated", errCrossDevice),
	}
	if _, err := replaceTreeMove(context.Background(), ws, src, dst); err == nil {
		t.Fatal("failed cross-device copy reported success")
	}
	data, readErr := os.ReadFile(filepath.Join(dst, "old.txt"))
	if readErr != nil || string(data) != "old" {
		t.Fatalf("old destination was not restored: %q, %v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(dst, "top.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("partial copy was left at the destination: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(src, "top.txt")); statErr != nil {
		t.Fatalf("copy failure damaged the source: %v", statErr)
	}
}

func TestCopyTreeForceKeepsDestinationWhenCopyFails(t *testing.T) {
	dir, ws := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	if err := os.Symlink("top.txt", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dst, "old.txt")
	if err := os.WriteFile(old, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &CopyTreeTool{WS: ws}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": src, "to": dst, "force": true,
	}))
	if err == nil {
		t.Fatal("copy_tree accepted a symlink in the source")
	}
	data, readErr := os.ReadFile(old)
	if readErr != nil || string(data) != "keep me" {
		t.Fatalf("failed force copy removed the old destination: %q, %v", data, readErr)
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Fatalf("failed force copy removed the source: %v", statErr)
	}
}

func TestCopyTreeForceReplaces(t *testing.T) {
	dir, ws := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &CopyTreeTool{WS: ws}
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": src, "to": dst, "force": true,
	})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("force copy kept the old destination file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "a", "b", "two.txt"))
	if err != nil || string(data) != "two\n" {
		t.Fatalf("force copy content = %q, %v", data, err)
	}
}

func TestMoveTreeForceReplaces(t *testing.T) {
	dir, ws := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &MoveTreeTool{WS: ws}
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": src, "to": dst, "force": true,
	})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists after force move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("force move kept the old destination file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "top.txt"))
	if err != nil || string(data) != "top\n" {
		t.Fatalf("force move content = %q, %v", data, err)
	}
}

func TestCopyTreeRefusesDestinationAncestor(t *testing.T) {
	dir, ws := treeWorkspace(t)
	src := filepath.Join(dir, "src")
	seedTree(t, src)
	tool := &CopyTreeTool{WS: ws}
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": src, "to": dir, "force": true,
	})); err == nil {
		t.Fatal("copy_tree allowed a destination that is an ancestor of the source")
	}
	if _, err := os.Stat(filepath.Join(src, "top.txt")); err != nil {
		t.Fatalf("ancestor check removed the source: %v", err)
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
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": src, "to": dst, "mkdir_p": true})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "x" {
		t.Fatalf("copied content = %q, %v", data, err)
	}
}
