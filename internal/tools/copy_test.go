package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostCopyTool builds a copy tool on the host workspace rooted at root.
func hostCopyTool(root string) *CopyTool {
	pv := NewPathValidator(root, false, nil)
	return &CopyTool{PV: pv, WS: NewLocalWorkspace(pv)}
}

func TestCopyTool_copiesByteExactAndKeepsSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "from.bin")
	dst := filepath.Join(dir, "nested", "to.bin")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, src, transferContent)

	if _, err := hostCopyTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": src, "to": dst})); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != string(transferContent) {
		t.Errorf("destination content = %q, want %q", got, transferContent)
	}
	orig, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("source must survive a copy: %v", err)
	}
	if string(orig) != string(transferContent) {
		t.Errorf("source content = %q, want %q", orig, transferContent)
	}
}

func TestCopyTool_clobberRules(t *testing.T) {
	cases := []struct {
		name  string
		force bool
		want  string
	}{
		{name: "refuses existing destination", force: false, want: "incumbent"},
		{name: "force replaces destination", force: true, want: string(transferContent)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src.bin")
			dst := filepath.Join(dir, "dst.bin")
			writeFile(t, src, transferContent)
			writeFile(t, dst, []byte("incumbent"))

			_, err := hostCopyTool(dir).Execute(context.Background(),
				transferArgs(t, map[string]any{"path": src, "to": dst, "force": tc.force}))
			if !tc.force && err == nil {
				t.Fatal("copying onto an existing destination without force must fail")
			}
			if tc.force && err != nil {
				t.Fatalf("force copy: %v", err)
			}
			got, readErr := os.ReadFile(dst)
			if readErr != nil {
				t.Fatalf("read destination: %v", readErr)
			}
			if string(got) != tc.want {
				t.Errorf("destination = %q, want %q", got, tc.want)
			}
			if _, statErr := os.Stat(src); statErr != nil {
				t.Errorf("source removed by a copy: %v", statErr)
			}
		})
	}
}

func TestCopyTool_requiresDestinationParent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	writeFile(t, src, []byte("x"))
	dst := filepath.Join(dir, "missing", "dst.txt")

	_, err := hostCopyTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": src, "to": dst}))
	if err == nil {
		t.Fatal("copying into a missing parent must fail rather than create it")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error = %q, want a pointer to mkdir", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("a refused copy created the destination: %v", statErr)
	}
}

// TestCopyTool_afterMkdir pins the explicit flow the two tools point at: the
// model creates the destination directory with mkdir, then copies into it.
func TestCopyTool_afterMkdir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "nested", "dst.txt")
	writeFile(t, src, []byte("payload"))

	if _, err := hostMkdirTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": filepath.Dir(dst)})); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := hostCopyTool(dir).Execute(context.Background(),
		transferArgs(t, map[string]any{"path": src, "to": dst})); err != nil {
		t.Fatalf("copy after mkdir: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("destination = %q, want %q", got, "payload")
	}
}

func TestCopyTool_refusesDirectories(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "tree")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file.txt")
	writeFile(t, file, []byte("x"))
	tool := hostCopyTool(dir)

	t.Run("directory source", func(t *testing.T) {
		if _, err := tool.Execute(context.Background(),
			transferArgs(t, map[string]any{"path": srcDir, "to": filepath.Join(dir, "copy-of-tree")})); err == nil {
			t.Fatal("copying a directory must be refused")
		}
	})
	t.Run("directory destination", func(t *testing.T) {
		if _, err := tool.Execute(context.Background(),
			transferArgs(t, map[string]any{"path": file, "to": srcDir, "force": true})); err == nil {
			t.Fatal("a destination that is a directory must be refused")
		}
	})
}

func TestCopyTool_pathValidatorDenial(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.txt")
	writeFile(t, src, []byte("notes"))

	pv := NewPathValidator(dir, false, []string{"*.secret"})
	tool := &CopyTool{PV: pv, WS: NewLocalWorkspace(pv)}
	if _, err := tool.Execute(context.Background(),
		transferArgs(t, map[string]any{"path": src, "to": filepath.Join(dir, "leak.secret")})); err == nil {
		t.Fatal("a deny pattern must refuse the destination")
	}
	if _, err := os.Stat(filepath.Join(dir, "leak.secret")); !os.IsNotExist(err) {
		t.Errorf("a refused copy created the destination: %v", err)
	}
}

func TestCopyTool_sandboxParity(t *testing.T) {
	ws := newSandboxWS(t, nil)
	ctx := context.Background()
	if err := ws.WriteFile(ctx, "/workspace/from.bin", transferContent, 0o640); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}
	tool := &CopyTool{WS: ws}

	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace/from.bin", "to": "/workspace/to.bin"})); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got, err := ws.ReadFile(ctx, "/workspace/to.bin")
	if err != nil {
		t.Fatalf("read sandbox destination: %v", err)
	}
	if string(got) != string(transferContent) {
		t.Errorf("sandbox destination content = %q, want %q", got, transferContent)
	}
	if _, err := ws.ReadFile(ctx, "/workspace/from.bin"); err != nil {
		t.Errorf("source must survive a sandbox copy: %v", err)
	}
	info, err := ws.Stat(ctx, "/workspace/to.bin")
	if err != nil {
		t.Fatalf("stat sandbox destination: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("copied mode = %o, want 640 (the source's mode)", perm)
	}

	// Force overwrite must also take the source's mode rather than keeping the
	// destination's old one; the sandbox applies it with chmod --reference after
	// cp, since plain cp would leave an existing destination's mode in place.
	if err := ws.WriteFile(ctx, "/workspace/private.bin", []byte("secret"), 0o600); err != nil {
		t.Fatalf("seed private source: %v", err)
	}
	if err := ws.WriteFile(ctx, "/workspace/shared.bin", []byte("old"), 0o644); err != nil {
		t.Fatalf("seed shared destination: %v", err)
	}
	if _, err := tool.Execute(ctx,
		transferArgs(t, map[string]any{"path": "/workspace/private.bin", "to": "/workspace/shared.bin", "force": true})); err != nil {
		t.Fatalf("force copy: %v", err)
	}
	overwritten, err := ws.Stat(ctx, "/workspace/shared.bin")
	if err != nil {
		t.Fatalf("stat overwritten destination: %v", err)
	}
	if perm := overwritten.Mode().Perm(); perm != 0o600 {
		t.Errorf("overwritten mode = %o, want 600 (the source's mode)", perm)
	}
}

func TestCopyTool_isDangerous(t *testing.T) {
	if !(&CopyTool{}).IsDangerous(`{}`) {
		t.Error("copy writes a file and must require approval")
	}
}
