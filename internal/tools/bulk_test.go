package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bulkWorkspace(t *testing.T) (string, Workspace) {
	t.Helper()
	dir := t.TempDir()
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	return pv.WorkspaceRoot, newLocalWorkspace(pv)
}

func seedBulk(t *testing.T, root string) {
	t.Helper()
	for _, rel := range []string{"a.txt", "b.txt", "sub/c.txt", "keep.go"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBulkFilesDryRunDoesNotMutate(t *testing.T) {
	dir, ws := bulkWorkspace(t)
	src := filepath.Join(dir, "src")
	seedBulk(t, src)
	tool := &BulkFilesTool{WS: ws}
	out, err := tool.Execute(context.Background(), `{"action":"copy","pattern":"*.txt","path":"`+src+`","to":"`+filepath.Join(dir, "out")+`","dry_run":true}`)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "copy") || !strings.Contains(out, "a.txt") {
		t.Fatalf("dry run output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "out")); !os.IsNotExist(err) {
		t.Fatalf("dry run created output: %v", err)
	}
	if tool.IsDangerous(`{"action":"delete","pattern":"*","dry_run":true}`) {
		t.Error("a dry run should not require approval")
	}
	if !tool.IsDangerous(`{"action":"delete","pattern":"*"}`) {
		t.Error("a real bulk change must require approval")
	}
}

func TestBulkFilesCopyPreservesRelativePath(t *testing.T) {
	dir, ws := bulkWorkspace(t)
	src := filepath.Join(dir, "src")
	seedBulk(t, src)
	tool := &BulkFilesTool{WS: ws}
	to := filepath.Join(dir, "out")
	out, err := tool.Execute(context.Background(), `{"action":"copy","pattern":"**/*.txt","path":"`+src+`","to":"`+to+`","mkdir_p":true}`)
	if err != nil {
		t.Fatalf("copy: %v (%s)", err, out)
	}
	for _, rel := range []string{"a.txt", "b.txt", "sub/c.txt"} {
		if _, err := os.Stat(filepath.Join(to, rel)); err != nil {
			t.Errorf("copied %s missing: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(to, "keep.go")); !os.IsNotExist(err) {
		t.Errorf("copy included non-matching file")
	}
}

func TestBulkFilesDeleteAndFailureReporting(t *testing.T) {
	dir, ws := bulkWorkspace(t)
	src := filepath.Join(dir, "src")
	seedBulk(t, src)
	tool := &BulkFilesTool{WS: ws}
	if _, err := tool.Execute(context.Background(), `{"action":"delete","pattern":"*.txt","path":"`+src+`"}`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, rel := range []string{"a.txt", "b.txt", "sub/c.txt"} {
		if _, err := os.Stat(filepath.Join(src, rel)); !os.IsNotExist(err) {
			t.Errorf("delete left %s", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(src, "keep.go")); err != nil {
		t.Errorf("delete removed keep.go: %v", err)
	}

	// A pre-existing destination without force is a partial failure: the
	// other file still copies, but the call reports a non-nil error.
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(dir, "out")
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, "a.txt"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(context.Background(), `{"action":"copy","pattern":"*.txt","path":"`+src+`","to":"`+to+`"}`)
	if err == nil {
		t.Fatalf("bulk copy with an existing destination reported success: %s", out)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error does not name the existing destination: %v", err)
	}
}
