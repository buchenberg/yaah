package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checksumWorkspace(t *testing.T) (string, Workspace) {
	t.Helper()
	dir := t.TempDir()
	pv := NewPathValidator(dir, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	return pv.WorkspaceRoot, newLocalWorkspace(pv)
}

func TestChecksumToolHashesAndCompares(t *testing.T) {
	dir, ws := checksumWorkspace(t)
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	c := filepath.Join(dir, "c.txt")
	_ = os.WriteFile(a, []byte("hello\n"), 0o644)
	_ = os.WriteFile(b, []byte("hello\n"), 0o600)
	_ = os.WriteFile(c, []byte("different\n"), 0o644)
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("hello\n")))

	tool := &ChecksumTool{WS: ws}
	out, err := tool.Execute(context.Background(), `{"paths":["`+a+`"]}`)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.Contains(out, want) || !strings.Contains(out, a) {
		t.Fatalf("hash output = %q", out)
	}

	out, err = tool.Execute(context.Background(), `{"paths":["`+a+`","`+b+`"],"compare":true}`)
	if err != nil {
		t.Fatalf("compare equal: %v", err)
	}
	if !strings.Contains(out, "equal: true") {
		t.Fatalf("compare equal output = %q", out)
	}
	out, err = tool.Execute(context.Background(), `{"paths":["`+a+`","`+c+`"],"compare":true}`)
	if err != nil {
		t.Fatalf("compare different: %v", err)
	}
	if !strings.Contains(out, "equal: false") {
		t.Fatalf("compare different output = %q", out)
	}
}

func TestChecksumToolValidation(t *testing.T) {
	dir, ws := checksumWorkspace(t)
	a := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(a, []byte("x"), 0o644)
	tool := &ChecksumTool{WS: ws}
	for _, args := range []string{
		`{"paths":[]}`,
		`{"paths":["` + a + `","` + a + `","` + a + `"],"compare":true}`,
		`{"paths":["` + dir + `"]}`,
	} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("checksum accepted %s", args)
		}
	}
	many := make([]string, checksumMaxPaths+1)
	for i := range many {
		many[i] = a
	}
	args, _ := json.Marshal(map[string]any{"paths": many})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Error("checksum accepted more than the batch limit")
	}
}
