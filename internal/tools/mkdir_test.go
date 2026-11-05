package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hostMkdirTool builds a mkdir tool on the host workspace rooted at root.
func hostMkdirTool(root string) *MkdirTool {
	pv := NewPathValidator(root, false, nil)
	return &MkdirTool{PV: pv, WS: NewLocalWorkspace(pv)}
}

func TestMkdirTool_createsNestedDirectories(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b", "c")

	result, err := hostMkdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": target}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "Created directory") {
		t.Errorf("result = %q, want a creation confirmation", result)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat created directory: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", target)
	}
}

func TestMkdirTool_existingDirectoryIsUntouched(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "private")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}

	result, err := hostMkdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": target}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "already exists") {
		t.Errorf("result = %q, want an already-exists message", result)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	// The host MkdirAll leaves an existing directory alone; the tool must not
	// chmod it as a side effect of an idempotent call. Windows maps modes to
	// 0777/0666 rather than preserving the POSIX bits, so the assertion only
	// applies where the mode is meaningful.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o700 {
		t.Errorf("existing directory mode = %o, want 700", perm)
	}
}

func TestMkdirTool_refusesFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "regular.txt")
	writeFile(t, file, []byte("x"))

	_, err := hostMkdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": file}))
	if err == nil {
		t.Fatal("mkdir over a regular file must be refused")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error = %q, want a not-a-directory refusal", err)
	}
}

func TestMkdirTool_pathValidatorDenial(t *testing.T) {
	dir := t.TempDir()
	pv := NewPathValidator(dir, false, []string{"*.secret"})
	tool := &MkdirTool{PV: pv, WS: NewLocalWorkspace(pv)}

	if _, err := tool.Execute(context.Background(),
		transferArgs(t, map[string]any{"path": filepath.Join(dir, "new.secret")})); err == nil {
		t.Fatal("a deny pattern must refuse the directory")
	}
	if _, err := tool.Execute(context.Background(),
		transferArgs(t, map[string]any{"path": filepath.Join(dir, "..", "escaped")})); err == nil {
		t.Fatal("a path outside the workspace root must be refused")
	}
}

func TestMkdirTool_sandboxParity(t *testing.T) {
	ws := newSandboxWS(t, nil)
	ctx := context.Background()
	tool := &MkdirTool{WS: ws}

	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace/nested/deeper"})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	info, err := ws.Stat(ctx, "/workspace/nested/deeper")
	if err != nil {
		t.Fatalf("stat created directory: %v", err)
	}
	if !info.Mode().IsDir() {
		t.Errorf("created entry = %v, want a directory", info.Mode())
	}

	// An idempotent call must not chmod an existing directory: the sandbox's
	// MkdirAll applies its mode even to an existing final directory, so the
	// tool's Stat-before-create is what protects a caller's mode.
	if err := ws.MkdirAll(ctx, "/workspace/private", 0o700); err != nil {
		t.Fatalf("seed private dir: %v", err)
	}
	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace/private"})); err != nil {
		t.Fatalf("Execute existing: %v", err)
	}
	info, err = ws.Stat(ctx, "/workspace/private")
	if err != nil {
		t.Fatalf("stat existing directory: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("existing sandbox directory mode = %o, want 700", perm)
	}
}

func TestMkdirTool_isDangerous(t *testing.T) {
	if !(&MkdirTool{}).IsDangerous(`{}`) {
		t.Error("mkdir mutates the workspace and must require approval")
	}
}

func TestMkdirTool_requiresPath(t *testing.T) {
	tool := hostMkdirTool(t.TempDir())
	for _, args := range []string{`{}`, `{"path":""}`, `not json`} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("args %q must be refused", args)
		}
	}
}

// TestMkdirTool_usesResolvedWorkspacePath pins that the tool creates the path
// the workspace resolved (including a relative path under the workspace root)
// rather than re-deriving one from the raw argument.
func TestMkdirTool_usesResolvedWorkspacePath(t *testing.T) {
	dir := t.TempDir()
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	tool := &MkdirTool{PV: pv, WS: NewLocalWorkspace(pv)}

	if _, err := tool.Execute(context.Background(),
		transferArgs(t, map[string]any{"path": "relative/dir"})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "relative", "dir"))
	if err != nil {
		t.Fatalf("stat relative directory: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("resolved relative path is not a directory: %v", info.Mode())
	}
}
