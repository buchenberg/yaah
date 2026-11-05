package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWakeGlobRelativeAndExact(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	green := filepath.Join(dir, "ci", "green")
	if err := os.WriteFile(green, []byte("state: green\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := newLocalWorkspace(&PathValidator{WorkspaceRoot: dir, WorkDir: dir})

	matches, err := wakeGlob(context.Background(), ws, "ci/*")
	if err != nil {
		t.Fatalf("wakeGlob(ci/*): %v", err)
	}
	if len(matches) != 1 || filepath.Clean(matches[0]) != filepath.Clean(green) {
		t.Fatalf("wakeGlob(ci/*) = %v, want [%s]", matches, green)
	}

	matches, err = wakeGlob(context.Background(), ws, "ci/green")
	if err != nil {
		t.Fatalf("wakeGlob(exact): %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("wakeGlob(exact) = %v, want one match", matches)
	}

	matches, err = wakeGlob(context.Background(), ws, "ci/missing")
	if err != nil {
		t.Fatalf("wakeGlob(missing): %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("wakeGlob(missing) = %v, want no matches", matches)
	}

	matches, err = wakeGlob(context.Background(), ws, "**/*.log")
	if err != nil {
		t.Fatalf("wakeGlob(**): %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("wakeGlob(**/*.log) = %v, want no matches", matches)
	}
}

func TestWakeDepsReadFileAndExec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello wake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := newLocalWorkspace(&PathValidator{WorkspaceRoot: dir, WorkDir: dir})
	deps := NewWakeDeps(ws, nil, nil)

	data, err := deps.ReadFile(context.Background(), "note.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello wake\n" {
		t.Fatalf("ReadFile = %q", data)
	}

	res, err := deps.Exec(context.Background(), "echo hi")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Output, "hi") {
		t.Fatalf("Exec = %+v", res)
	}
}

func TestWakeDepsRespectWorkspaceContainment(t *testing.T) {
	dir := t.TempDir()
	ws := newLocalWorkspace(&PathValidator{WorkspaceRoot: dir})
	if _, err := wakeGlob(context.Background(), ws, "../*"); err == nil {
		t.Fatal("wakeGlob escaped the workspace root")
	}
}
