package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runPatch(t *testing.T, file, patch string) (string, error) {
	t.Helper()
	pt := &PatchTool{}
	args, _ := json.Marshal(map[string]string{"filePath": file, "patch": patch})
	return pt.Execute(context.Background(), string(args))
}

// TestPatchTool_exactHunk applies a clean hunk with context lines.
func TestPatchTool_exactHunk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	os.WriteFile(path, []byte("alpha\nbeta\ngamma\ndelta\n"), 0o644)

	patch := `@@ -1,4 +1,4 @@
 alpha
-beta
+BETA
 gamma
 delta
`
	if _, err := runPatch(t, path, patch); err != nil {
		t.Fatalf("patch: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "BETA") || strings.Contains(string(data), "\nbeta\n") {
		t.Errorf("expected beta→BETA, got %q", data)
	}
}

// TestPatchTool_addAndRemove pins hunk ops composition: '-' lines are
// consumed, '+' lines are inserted, context lines are preserved.
func TestPatchTool_addAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644)

	patch := `@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
+four
`
	if _, err := runPatch(t, path, patch); err != nil {
		t.Fatalf("patch: %v", err)
	}
	data, _ := os.ReadFile(path)
	want := "one\nTWO\nthree\nfour\n"
	if string(data) != want {
		t.Errorf("content = %q, want %q", data, want)
	}
}

// TestPatchTool_whitespaceTolerant pins the tolerant matching: a hunk
// whose context lines differ only in leading/trailing whitespace still
// applies (matchHunk trims for comparison).
func TestPatchTool_whitespaceTolerant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	os.WriteFile(path, []byte("func a() {\n    x := 1\n}\n"), 0o644)

	// Patch context uses tabs; file uses spaces.
	patch := "@@ -1,3 +1,3 @@\n func a() {\n-\tx := 1\n+\tx := 2\n }\n"
	if _, err := runPatch(t, path, patch); err != nil {
		t.Fatalf("whitespace-tolerant hunk should apply: %v", err)
	}
}

// TestPatchTool_nearMissMustNotApply pins the safety property: a hunk
// whose context does not match any location must fail, not corrupt.
func TestPatchTool_nearMissMustNotApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	original := "alpha\nbeta\ngamma\n"
	os.WriteFile(path, []byte(original), 0o644)

	patch := `@@ -1,3 +1,3 @@
 alpha
-notbeta
+NOTBETA
 gamma
`
	if _, err := runPatch(t, path, patch); err == nil {
		t.Fatal("near-miss hunk must not apply")
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Errorf("file was modified by a failed patch: %q", data)
	}
}

// TestPatchTool_secondHunk pins sequential multi-hunk application.
func TestPatchTool_secondHunk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	os.WriteFile(path, []byte("one\ntwo\nthree\nfour\nfive\n"), 0o644)

	patch := `@@ -1,2 +1,2 @@
 one
-two
+TWO
@@ -4,2 +4,2 @@
 four
-five
+FIVE
`
	if _, err := runPatch(t, path, patch); err != nil {
		t.Fatalf("patch: %v", err)
	}
	data, _ := os.ReadFile(path)
	want := "one\nTWO\nthree\nfour\nFIVE\n"
	if string(data) != want {
		t.Errorf("content = %q, want %q", data, want)
	}
}

// TestPatchTool_targetFromHeader pins the +++ b/ header parsing when
// filePath is omitted. The parsed relative path resolves against the
// process working directory, so the test chdirs to the temp dir.
func TestPatchTool_targetFromHeader(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "header.txt")
	os.WriteFile(path, []byte("old\n"), 0o644)

	patch := "--- a/header.txt\n+++ b/header.txt\n@@ -1 +1 @@\n-old\n+new\n"
	pt := &PatchTool{}
	args, _ := json.Marshal(map[string]string{"patch": patch})
	if _, err := pt.Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("patch: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new\n" {
		t.Errorf("content = %q, want %q", data, "new\n")
	}
}

// TestPatchTool_missingFileErrors pins a clear error on an absent
// target rather than a corrupt partial apply.
func TestPatchTool_missingFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.txt")
	patch := "@@ -1 +1 @@\n-old\n+new\n"
	if _, err := runPatch(t, path, patch); err == nil {
		t.Fatal("patch on a missing file should error")
	}
}
