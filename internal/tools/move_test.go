package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// transferContent is deliberately not plain ASCII. A byte-fidelity test over
// "hello" cannot catch a transform — a TrimSpace, a CRLF rewrite, a text-mode
// read that mangles invalid UTF-8 — that a NUL, a bare \xff, and CRLF can.
var transferContent = []byte("line one\r\nline two\n\x00\xff\xfe tail\n")

// hostMoveTool builds a move tool on the host workspace, optionally under a
// workspace root so PathValidator containment is exercised.
func hostMoveTool(root string) *MoveTool {
	pv := NewPathValidator(root, false, nil)
	return &MoveTool{PV: pv, WS: NewLocalWorkspace(pv)}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func transferArgs(t *testing.T, params map[string]any) string {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(b)
}

func TestMoveTool_movesFileByteExact(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "from.bin")
	dst := filepath.Join(dir, "sub-way", "to.bin")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, src, transferContent)

	if _, err := hostMoveTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": src, "to": dst})); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := os.Stat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("source still present after move: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != string(transferContent) {
		t.Errorf("destination content = %q, want %q", got, transferContent)
	}
}

func TestMoveTool_clobberRules(t *testing.T) {
	cases := []struct {
		name  string
		force bool
		want  []byte // nil means "the move must fail and dst is unchanged"
	}{
		{name: "refuses existing destination", force: false, want: nil},
		{name: "force replaces destination", force: true, want: transferContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src.txt")
			dst := filepath.Join(dir, "dst.txt")
			writeFile(t, src, transferContent)
			writeFile(t, dst, []byte("incumbent"))

			_, err := hostMoveTool(dir).Execute(context.Background(),
				transferArgs(t, map[string]any{"path": src, "to": dst, "force": tc.force}))
			if tc.want == nil {
				if err == nil {
					t.Fatal("moving onto an existing destination without force must fail")
				}
				if got, _ := os.ReadFile(dst); string(got) != "incumbent" {
					t.Errorf("destination changed on a refused move: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("force move: %v", err)
			}
			if got, _ := os.ReadFile(dst); string(got) != string(tc.want) {
				t.Errorf("destination = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMoveTool_refusesDirectories(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "tree")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dstDir := filepath.Join(dir, "other-tree")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file.txt")
	writeFile(t, file, []byte("x"))

	t.Run("directory source", func(t *testing.T) {
		_, err := hostMoveTool(dir).Execute(context.Background(),
			transferArgs(t, map[string]any{"path": srcDir, "to": filepath.Join(dir, "moved-tree")}))
		if err == nil {
			t.Fatal("moving a directory must be refused")
		}
		if _, statErr := os.Stat(srcDir); statErr != nil {
			t.Errorf("directory moved despite the refusal: %v", statErr)
		}
	})
	t.Run("directory destination", func(t *testing.T) {
		_, err := hostMoveTool(dir).Execute(context.Background(),
			transferArgs(t, map[string]any{"path": file, "to": dstDir, "force": true}))
		if err == nil {
			t.Fatal("a destination that is a directory must be refused")
		}
	})
	t.Run("non-regular source", func(t *testing.T) {
		// A dangling symlink is the portable stand-in for the rule that also
		// refuses FIFOs and devices: the path layer resolves a valid symlink to
		// its target (as for every file tool) but leaves a dangling link
		// untouched, so the transfer sees the link itself and refuses it.
		link := filepath.Join(dir, "dangling-src")
		if err := os.Symlink(filepath.Join(dir, "no-such-file"), link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := hostMoveTool(dir).Execute(context.Background(),
			transferArgs(t, map[string]any{"path": link, "to": filepath.Join(dir, "out.txt")})); err == nil {
			t.Fatal("a non-regular source must be refused")
		}
	})
	t.Run("symlink destination", func(t *testing.T) {
		// Same mechanics on the destination side: a dangling link resolves to
		// itself and is refused outright, even with force.
		link := filepath.Join(dir, "dangling-dst")
		if err := os.Symlink(filepath.Join(dir, "no-such-file"), link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := hostMoveTool(dir).Execute(context.Background(),
			transferArgs(t, map[string]any{"path": file, "to": link, "force": true})); err == nil {
			t.Fatal("a symlink destination must be refused even with force")
		}
	})
}

func TestMoveTool_pathValidatorDenial(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "keep.sh")
	writeFile(t, src, []byte("#!/bin/sh\n"))

	pv := NewPathValidator(dir, false, []string{"*.secret"})
	tool := &MoveTool{PV: pv, WS: NewLocalWorkspace(pv)}
	_, err := tool.Execute(context.Background(),
		transferArgs(t, map[string]any{"path": src, "to": filepath.Join(dir, "out.secret")}))
	if err == nil {
		t.Fatal("a deny pattern must refuse the destination")
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Errorf("source removed by a refused move: %v", statErr)
	}

	// Containment: an escape out of the workspace root is refused too.
	_, err = tool.Execute(context.Background(),
		transferArgs(t, map[string]any{"path": src, "to": filepath.Join(dir, "..", "escaped.sh")}))
	if err == nil {
		t.Fatal("a path outside the workspace root must be refused")
	}
}

func TestMoveTool_sandboxParity(t *testing.T) {
	ws := newSandboxWS(t, nil)
	ctx := context.Background()
	if err := ws.WriteFile(ctx, "/workspace/from.bin", transferContent, 0o644); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}
	tool := &MoveTool{WS: ws}

	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace/from.bin", "to": "/workspace/to.bin"})); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := ws.Stat(ctx, "/workspace/from.bin"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("source still present in the sandbox: %v", err)
	}
	got, err := ws.ReadFile(ctx, "/workspace/to.bin")
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != string(transferContent) {
		t.Errorf("sandbox destination content = %q, want %q", got, transferContent)
	}

	// The no-clobber rule is the tool's, so it holds over the sandbox too.
	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace/to.bin", "to": "/workspace/to.bin"})); err == nil {
		t.Error("moving onto an existing destination must be refused in a sandbox too")
	}
}

func TestMoveTool_isDangerous(t *testing.T) {
	if !(&MoveTool{}).IsDangerous(`{}`) {
		t.Error("move relocates a user file and must require approval")
	}
}

func TestMoveTool_requiresBothPaths(t *testing.T) {
	tool := hostMoveTool(t.TempDir())
	for _, args := range []string{`{}`, `{"path":"/tmp/x"}`, `{"to":"/tmp/x"}`, `not json`} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("args %q must be refused", args)
		}
	}
}
