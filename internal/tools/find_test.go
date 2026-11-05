package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func findWorkspace(t *testing.T) (string, Workspace) {
	t.Helper()
	dir := t.TempDir()
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	return pv.WorkspaceRoot, newLocalWorkspace(pv)
}

func TestFindFiltersByNameSizeTypeAndAge(t *testing.T) {
	dir, ws := findWorkspace(t)
	old := time.Now().Add(-48 * time.Hour)
	files := map[string]struct {
		size int
		old  bool
	}{
		"small.go":       {3, false},
		"large.go":       {200, false},
		"old.go":         {10, true},
		"notes.txt":      {4, false},
		"nested/deep.go": {5, false},
	}
	for rel, spec := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Repeat("x", spec.size)), 0o644); err != nil {
			t.Fatal(err)
		}
		if spec.old {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	tool := &FindTool{WS: ws}

	out, err := tool.Execute(context.Background(), `{"path":"`+dir+`","name":"*.go","type":"file"}`)
	if err != nil {
		t.Fatalf("name/type: %v", err)
	}
	for _, want := range []string{"small.go", "large.go", "old.go", "nested/deep.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("result %q missing %s", out, want)
		}
	}
	if strings.Contains(out, "notes.txt") {
		t.Errorf("result included a non-.go file: %q", out)
	}

	out, err = tool.Execute(context.Background(), `{"path":"`+dir+`","min_size":100}`)
	if err != nil {
		t.Fatalf("min_size: %v", err)
	}
	if !strings.Contains(out, "large.go") || strings.Contains(out, "small.go") {
		t.Errorf("min_size result = %q", out)
	}

	out, err = tool.Execute(context.Background(), `{"path":"`+dir+`","older_than":"24h"}`)
	if err != nil {
		t.Fatalf("older_than: %v", err)
	}
	if !strings.Contains(out, "old.go") || strings.Contains(out, "small.go") {
		t.Errorf("older_than result = %q", out)
	}

	out, err = tool.Execute(context.Background(), `{"path":"`+dir+`","type":"dir"}`)
	if err != nil {
		t.Fatalf("type dir: %v", err)
	}
	if !strings.Contains(out, "nested") {
		t.Errorf("dir result = %q", out)
	}
}
