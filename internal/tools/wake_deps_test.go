package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wakeTestWorkspace builds a real workspace the way a session does:
// NewPathValidator canonicalizes the root (macOS /var → /private/var,
// Windows 8.3 names → long names), and WorkDir points at that same spelling
// so relative paths and the containment check agree.
func wakeTestWorkspace(t *testing.T) (string, Workspace) {
	t.Helper()
	pv := NewPathValidator(t.TempDir(), false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	return pv.WorkspaceRoot, newLocalWorkspace(pv)
}

func TestWakeGlobRelativeAndExact(t *testing.T) {
	dir, ws := wakeTestWorkspace(t)
	if err := os.MkdirAll(filepath.Join(dir, "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	green := filepath.Join(dir, "ci", "green")
	if err := os.WriteFile(green, []byte("state: green\n"), 0o644); err != nil {
		t.Fatal(err)
	}

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

func TestSplitGlobRoots(t *testing.T) {
	cases := []struct {
		pattern string
		root    string
		magic   string
	}{
		{"src/**/*.ts", "src", "**/*.ts"},
		{"*.log", ".", "*.log"},
		{".*", ".", ".*"},
		{"/*.log", "/", "*.log"},
		{"/**/*.go", "/", "**/*.go"},
		{"ci/../logs/*.log", "ci/../logs", "*.log"},
		{"link/**/*.go", "link", "**/*.go"},
	}
	for _, tc := range cases {
		root, magic, err := splitGlob(tc.pattern)
		if err != nil {
			t.Errorf("splitGlob(%q): %v", tc.pattern, err)
			continue
		}
		if root != tc.root || magic != tc.magic {
			t.Errorf("splitGlob(%q) = (%q, %q), want (%q, %q)", tc.pattern, root, magic, tc.root, tc.magic)
		}
	}
}

func TestWakeGlobDotfiles(t *testing.T) {
	dir, ws := wakeTestWorkspace(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	matches, err := wakeGlob(context.Background(), ws, ".*")
	if err != nil {
		t.Fatalf("wakeGlob(.*): %v", err)
	}
	if len(matches) != 1 || filepath.Base(matches[0]) != ".env" {
		t.Fatalf("wakeGlob(.*) = %v, want [.env]", matches)
	}
}

func TestWakeGlobThroughSymlinkRoot(t *testing.T) {
	dir, ws := wakeTestWorkspace(t)
	if err := os.MkdirAll(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real", "ci.log"), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	matches, err := wakeGlob(context.Background(), ws, "link/*.log")
	if err != nil {
		t.Fatalf("wakeGlob(link/*.log): %v", err)
	}
	if len(matches) != 1 || filepath.Base(matches[0]) != "ci.log" {
		t.Fatalf("wakeGlob(link/*.log) = %v, want [ci.log]", matches)
	}
}

func TestWakeGlobDotDotRoot(t *testing.T) {
	dir, ws := wakeTestWorkspace(t)
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "ci.log"), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	matches, err := wakeGlob(context.Background(), ws, "missing/../logs/*.log")
	if err != nil {
		t.Fatalf("wakeGlob(missing/../logs/*.log): %v", err)
	}
	if len(matches) != 1 || filepath.Base(matches[0]) != "ci.log" {
		t.Fatalf("wakeGlob(missing/../logs/*.log) = %v, want [ci.log]", matches)
	}
}

func TestWakeDepsReadFileAndExec(t *testing.T) {
	dir, ws := wakeTestWorkspace(t)
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello wake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	_, ws := wakeTestWorkspace(t)
	if _, err := wakeGlob(context.Background(), ws, "../*"); err == nil {
		t.Fatal("wakeGlob escaped the workspace root")
	}
	if _, err := NewWakeDeps(ws, nil, nil).ReadFile(context.Background(), "../outside"); err == nil {
		t.Fatal("ReadFile escaped the workspace root")
	}
}
