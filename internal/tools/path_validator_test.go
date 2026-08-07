package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathValidator_NoRestriction(t *testing.T) {
	pv := NewPathValidator("", false, nil)
	dir := t.TempDir()

	got, err := pv.ResolvePath(dir)
	if err != nil {
		t.Fatalf("ResolvePath(%q) error: %v", dir, err)
	}
	if got == "" {
		t.Error("expected non-empty resolved path")
	}
}

func TestPathValidator_ContainsWorkspace(t *testing.T) {
	ws := t.TempDir()
	if real, err := filepath.EvalSymlinks(ws); err == nil {
		ws = real
	}
	pv := NewPathValidator(ws, false, nil)

	inside := filepath.Join(ws, "sub", "file.txt")
	got, err := pv.ResolvePath(inside)
	if err != nil {
		t.Fatalf("inside path rejected: %v", err)
	}
	if !strings.HasPrefix(got, pv.WorkspaceRoot) {
		t.Errorf("resolved %q not inside workspace %q", got, pv.WorkspaceRoot)
	}

	if _, err := pv.ResolvePath(filepath.Join(ws, "..", "outside.txt")); err == nil {
		t.Error("parent-escape path must be rejected")
	}

	other := t.TempDir()
	if _, err := pv.ResolvePath(filepath.Join(other, "foreign.txt")); err == nil {
		t.Error("path in another directory must be rejected")
	}
}

func TestPathValidator_RelativePathResolvesAgainstCWD(t *testing.T) {
	ws := t.TempDir()
	pv := NewPathValidator(ws, false, nil)

	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot determine cwd: %v", err)
	}
	if rel, relErr := filepath.Rel(ws, wd); relErr == nil && !strings.HasPrefix(rel, "..") {
		t.Skip("cwd is inside the workspace; cannot test crossing")
	}

	if _, err := pv.ResolvePath("some/relative/path.txt"); err == nil {
		t.Error("relative path resolved outside the workspace must be rejected")
	}
}

func TestPathValidator_SymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(ws, "link.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	pv := NewPathValidator(ws, false, nil)
	if _, err := pv.ResolvePath(link); err == nil {
		t.Error("symlink escaping the workspace must be rejected")
	}
}

func TestPathValidator_HomeAccess(t *testing.T) {
	ws := t.TempDir()

	denied := NewPathValidator(ws, false, nil)
	if _, err := denied.ResolvePath("~/notes.txt"); err == nil {
		t.Error("~ must be rejected when AllowHomeAccess is false")
	}

	allowed := NewPathValidator("", true, nil)
	got, err := allowed.ResolvePath("~")
	if err != nil {
		t.Fatalf("~ with AllowHomeAccess: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if got != home {
		t.Errorf("ResolvePath(~) = %q, want home %q", got, home)
	}
}

func TestPathValidator_DenyPatterns(t *testing.T) {
	ws := t.TempDir()
	pv := NewPathValidator(ws, false, []string{".env", "*.pem"})

	if _, err := pv.ResolvePath(filepath.Join(ws, ".env")); err == nil {
		t.Error(".env must be denied")
	}
	if _, err := pv.ResolvePath(filepath.Join(ws, "certs", "key.pem")); err == nil {
		t.Error("*.pem must be denied")
	}
	if _, err := pv.ResolvePath(filepath.Join(ws, "config.yaml")); err != nil {
		t.Errorf("config.yaml wrongly denied: %v", err)
	}
}

func TestPathValidator_AskGrantsException(t *testing.T) {
	ws := t.TempDir()
	if real, err := filepath.EvalSymlinks(ws); err == nil {
		ws = real
	}
	outsideDir := t.TempDir()
	if real, err := filepath.EvalSymlinks(outsideDir); err == nil {
		outsideDir = real
	}
	outside := filepath.Join(outsideDir, "file.txt")

	var gotPath, gotReason string
	pv := NewPathValidator(ws, false, nil)
	pv.AskFn = func(path, reason string) bool {
		gotPath, gotReason = path, reason
		return true
	}

	if _, err := pv.ResolvePath(outside); err != nil {
		t.Fatalf("ask-granted path still rejected: %v", err)
	}
	if gotPath == "" {
		t.Error("AskFn not called (empty path)")
	}
	if !strings.Contains(gotReason, "outside the workspace") {
		t.Errorf("AskFn reason = %q, want workspace mention", gotReason)
	}
}

func TestPathValidator_AskDenyKeepsReject(t *testing.T) {
	ws := t.TempDir()
	pv := NewPathValidator(ws, false, nil)
	pv.AskFn = func(path, reason string) bool { return false }

	if _, err := pv.ResolvePath(filepath.Join(ws, "..", "nope.txt")); err == nil {
		t.Error("denied ask must still reject")
	}
}

func TestPathValidator_AskResultIsCached(t *testing.T) {
	ws := t.TempDir()
	outside := filepath.Join(t.TempDir(), "file.txt")

	calls := 0
	pv := NewPathValidator(ws, false, nil)
	pv.AskFn = func(path, reason string) bool {
		calls++
		return true
	}

	for i := 0; i < 3; i++ {
		if _, err := pv.ResolvePath(outside); err != nil {
			t.Fatalf("resolve %d failed: %v", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("AskFn called %d times, want 1 (grants are cached)", calls)
	}
}

func TestPathValidator_AskHomeAccess(t *testing.T) {
	pv := NewPathValidator("", false, nil)
	pv.AskFn = func(path, reason string) bool {
		return strings.Contains(reason, "home-directory")
	}

	if _, err := pv.ResolvePath("~"); err != nil {
		t.Fatalf("ask-granted home access rejected: %v", err)
	}
}

func TestPathValidator_AskDenyPattern(t *testing.T) {
	ws := t.TempDir()
	if real, err := filepath.EvalSymlinks(ws); err == nil {
		ws = real
	}
	pv := NewPathValidator(ws, false, []string{".env"})
	pv.AskFn = func(path, reason string) bool {
		return strings.Contains(reason, "deny pattern")
	}

	if _, err := pv.ResolvePath(filepath.Join(ws, ".env")); err != nil {
		t.Fatalf("ask-granted deny-pattern path rejected: %v", err)
	}
	// Other deny-pattern paths still prompt independently.
	if _, err := pv.ResolvePath(filepath.Join(ws, "sub", ".env")); err != nil {
		t.Fatalf("second .env path should prompt and pass: %v", err)
	}
}

func TestRegistry_SetPathValidatorBackfills(t *testing.T) {
	reg := NewRegistry()
	ws := t.TempDir()
	reg.SetPathValidator(NewPathValidator(ws, false, nil))

	rt, ok := reg.Get("read").(*ReadTool)
	if !ok {
		t.Fatal("read tool not registered as *ReadTool")
	}
	if rt.PV == nil || rt.PV.WorkspaceRoot == "" {
		t.Error("SetPathValidator did not backfill the read tool")
	}

	reg.Register(&WriteTool{})
	wt, ok := reg.Get("write").(*WriteTool)
	if !ok {
		t.Fatal("write tool not registered as *WriteTool")
	}
	if wt.PV == nil {
		t.Error("Register after SetPathValidator did not auto-inject")
	}
}

func TestReadTool_WorkspaceEnforced(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(ws, "ok.txt")
	if err := os.WriteFile(inside, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	rt := &ReadTool{PV: NewPathValidator(ws, false, nil)}

	if _, err := rt.Execute(context.Background(), `{"filePath":"`+filepath.ToSlash(secret)+`"}`); err == nil {
		t.Error("read outside workspace must fail")
	}
	res, err := rt.Execute(context.Background(), `{"filePath":"`+filepath.ToSlash(inside)+`"}`)
	if err != nil {
		t.Fatalf("read inside workspace failed: %v", err)
	}
	if !strings.Contains(res, "hello") {
		t.Errorf("unexpected content: %q", res)
	}
}

func TestWriteTool_WorkspaceEnforced(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()

	wt := &WriteTool{PV: NewPathValidator(ws, false, nil)}

	target := filepath.Join(outside, "evil.txt")
	if _, err := wt.Execute(context.Background(), `{"filePath":"`+filepath.ToSlash(target)+`","content":"x"}`); err == nil {
		t.Fatal("write outside workspace must fail")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("file outside workspace was created despite rejection")
	}
}
