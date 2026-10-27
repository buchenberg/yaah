package tools

// conformance_test.go is the executable form of the result contract in
// tools.go: a nil error from Execute means the operation succeeded. Each
// multi-step tool must have at least one failing scenario here that proves a
// partial or injected failure comes back as a non-nil error, not as prose
// inside a successful result.
//
// Add a case whenever a tool gains a partial-application path.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// multiStepTools is the coverage list the conformance table must satisfy.
var multiStepTools = []string{"edit", "json_query", "patch", "replace"}

// faultWorkspace delegates to a real workspace but injects a read or write
// failure for one file name. Tests use it instead of chmod, which is a no-op
// for a privileged CI user.
type faultWorkspace struct {
	Workspace
	failBase string
	readErr  error
	writeErr error
}

func (w *faultWorkspace) fails(path string) bool {
	return w.failBase != "" && filepath.Base(path) == w.failBase
}

func (w *faultWorkspace) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if w.readErr != nil && w.fails(path) {
		return nil, w.readErr
	}
	return w.Workspace.ReadFile(ctx, path)
}

func (w *faultWorkspace) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if w.writeErr != nil && w.fails(path) {
		return w.writeErr
	}
	return w.Workspace.WriteFile(ctx, path, data, perm)
}

type conformanceCase struct {
	toolName string
	name     string
	setup    func(t *testing.T) (Tool, string)
	want     string
}

func TestToolConformance_FailingScenarioReturnsError(t *testing.T) {
	cases := []conformanceCase{
		{
			toolName: "replace",
			name:     "per-file write failure is an error, not a success line",
			setup: func(t *testing.T) (Tool, string) {
				dir := t.TempDir()
				writeFileAt(t, filepath.Join(dir, "a.txt"), "foo\n")
				writeFileAt(t, filepath.Join(dir, "b.txt"), "foo\n")
				ws := &faultWorkspace{
					Workspace: newLocalWorkspace(nil),
					failBase:  "a.txt",
					writeErr:  errors.New("injected write failure"),
				}
				args, _ := json.Marshal(map[string]any{
					"pattern": "foo", "replacement": "bar", "path": dir,
				})
				return &ReplaceTool{WS: ws}, string(args)
			},
			want: "injected write failure",
		},
		{
			toolName: "replace",
			name:     "per-file read failure is an error",
			setup: func(t *testing.T) (Tool, string) {
				dir := t.TempDir()
				writeFileAt(t, filepath.Join(dir, "a.txt"), "foo\n")
				ws := &faultWorkspace{
					Workspace: newLocalWorkspace(nil),
					failBase:  "a.txt",
					readErr:   errors.New("injected read failure"),
				}
				args, _ := json.Marshal(map[string]any{
					"pattern": "foo", "replacement": "bar", "path": dir,
				})
				return &ReplaceTool{WS: ws}, string(args)
			},
			want: "injected read failure",
		},
		{
			toolName: "patch",
			name:     "hunk mismatch leaves the target untouched and errors",
			setup: func(t *testing.T) (Tool, string) {
				path := filepath.Join(t.TempDir(), "f.txt")
				writeFileAt(t, path, "alpha\nbeta\ngamma\n")
				args, _ := json.Marshal(map[string]string{
					"filePath": path,
					"patch":    "@@ -1,3 +1,3 @@\n alpha\n-notbeta\n+NOTBETA\n gamma\n",
				})
				return &PatchTool{}, string(args)
			},
			want: "left unchanged",
		},
		{
			toolName: "patch",
			name:     "hunkless input is rejected instead of overwriting the file",
			setup: func(t *testing.T) (Tool, string) {
				path := filepath.Join(t.TempDir(), "f.txt")
				writeFileAt(t, path, "original\n")
				args, _ := json.Marshal(map[string]string{
					"filePath": path,
					"patch":    "not a unified diff\n+just some text\n",
				})
				return &PatchTool{}, string(args)
			},
			want: "no @@ hunks",
		},
		{
			toolName: "patch",
			name:     "multi-file diff is rejected",
			setup: func(t *testing.T) (Tool, string) {
				path := filepath.Join(t.TempDir(), "a.txt")
				writeFileAt(t, path, "x\n")
				args, _ := json.Marshal(map[string]string{
					"filePath": path,
					"patch":    "--- a/a.txt\n+++ b/a.txt\n--- a/b.txt\n+++ b/b.txt\n@@ -1 +1 @@\n-x\n+y\n",
				})
				return &PatchTool{}, string(args)
			},
			want: "multi-file diffs are not supported",
		},
		{
			toolName: "json_query",
			name:     "write failure is an error",
			setup: func(t *testing.T) (Tool, string) {
				path := filepath.Join(t.TempDir(), "cfg.json")
				writeFileAt(t, path, `{"name":"old"}`)
				ws := &faultWorkspace{
					Workspace: newLocalWorkspace(nil),
					failBase:  "cfg.json",
					writeErr:  errors.New("injected write failure"),
				}
				args, _ := json.Marshal(map[string]any{
					"file": path, "path": "name", "set": `"new"`,
				})
				return &JSONQueryTool{WS: ws}, string(args)
			},
			want: "injected write failure",
		},
		{
			toolName: "json_query",
			name:     "delete of a missing key is an error",
			setup: func(t *testing.T) (Tool, string) {
				path := filepath.Join(t.TempDir(), "cfg.json")
				writeFileAt(t, path, `{"name":"old"}`)
				args, _ := json.Marshal(map[string]any{
					"file": path, "path": "missing", "action": "delete",
				})
				return &JSONQueryTool{}, string(args)
			},
			want: "not found",
		},
		{
			toolName: "edit",
			name:     "all multi-edits failing is an error",
			setup: func(t *testing.T) (Tool, string) {
				path := filepath.Join(t.TempDir(), "f.txt")
				writeFileAt(t, path, "alpha\n")
				args, _ := json.Marshal(map[string]any{
					"filePath": path,
					"edits":    []map[string]string{{"oldString": "absent", "newString": "x"}},
				})
				return &EditTool{}, string(args)
			},
			want: "all 1 edits failed",
		},
	}

	seen := make(map[string]bool)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, args := tc.setup(t)
			if _, err := tool.Execute(context.Background(), args); err == nil {
				t.Fatal("Execute() returned a nil error for a failing scenario; a nil error must mean success")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
			seen[tc.toolName] = true
		})
	}

	for _, name := range multiStepTools {
		if !seen[name] {
			t.Errorf("multi-step tool %q has no conformance case", name)
		}
	}
}

func writeFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("%s = %q, want %q", path, data, want)
	}
}
