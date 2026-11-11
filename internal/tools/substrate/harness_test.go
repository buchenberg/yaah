package substrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// harness_test.go pins the harness-configuration guard: global config and
// role/skill files are denied outright, workspace copies require approval, and
// ordinary source files are untouched. The guard is independent of the
// permission ruleset and of approval mode by construction, so these tests
// exercise it directly rather than through classifyGate.

func newGuardFixture(t *testing.T) (home, configDir, ws string, h *HarnessPaths) {
	t.Helper()
	home = t.TempDir()
	configDir = filepath.Join(home, ".yaah")
	ws = t.TempDir()
	return home, configDir, ws, NewHarnessPaths(home, configDir, ws)
}

func TestHarnessPaths_Classify(t *testing.T) {
	home, configDir, ws, h := newGuardFixture(t)

	cases := []struct {
		name string
		path string
		want HarnessVerdict
	}{
		{"global role file", filepath.Join(home, ".agents", "roles", "auditor.md"), HarnessDeny},
		{"global skill", filepath.Join(home, ".agents", "skills", "deploy", "SKILL.md"), HarnessDeny},
		{"global plan", filepath.Join(home, ".agents", "plans", "p.md"), HarnessAsk},
		{"config file", filepath.Join(configDir, "config.yaml"), HarnessDeny},
		{"state db", filepath.Join(configDir, "trace.db"), HarnessDeny},
		{"project role file", filepath.Join(ws, ".agents", "roles", "dev.md"), HarnessAsk},
		{"project skill", filepath.Join(ws, ".agents", "skills", "s", "SKILL.md"), HarnessAsk},
		{"project plan", filepath.Join(ws, ".agents", "plans", "p.md"), HarnessAsk},
		{"session state", filepath.Join(ws, ".yaah", "spill", "x.md"), HarnessAsk},
		{"agents tree root", filepath.Join(ws, ".agents"), HarnessAsk},
		{"agents fallback", filepath.Join(ws, ".agents", "roles-archive", "x.md"), HarnessAsk},
		{"user agents tree root", filepath.Join(home, ".agents"), HarnessDeny},
		{"ordinary source", filepath.Join(ws, "src", "main.go"), HarnessAllow},
		{"prefix boundary", filepath.Join(ws, ".agents-archive", "roles", "x.md"), HarnessAllow},
		{"outside workspace", filepath.Join(ws, "..", "elsewhere", ".agents", "roles", "x.md"), HarnessAllow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := h.Classify(tc.path)
			if got != tc.want {
				t.Fatalf("Classify(%s) = %v (%q), want %v", tc.path, got, reason, tc.want)
			}
			if got != HarnessAllow && reason == "" {
				t.Errorf("Classify(%s) returned a verdict with no reason", tc.path)
			}
		})
	}
}

func TestHarnessPaths_HomeBeatsWorkspace(t *testing.T) {
	// A session started in $HOME must not downgrade global role protection
	// to a workspace ask: global rules are ordered first.
	home := t.TempDir()
	h := NewHarnessPaths(home, filepath.Join(home, ".yaah"), home)
	got, _ := h.Classify(filepath.Join(home, ".agents", "roles", "x.md"))
	if got != HarnessDeny {
		t.Fatalf("home workspace role = %v, want deny", got)
	}
}

func TestHarnessPaths_CheckWrite(t *testing.T) {
	home, _, ws, h := newGuardFixture(t)

	globalRole := filepath.Join(home, ".agents", "roles", "x.md")
	var refusal *HarnessRefusalError
	if err := h.CheckWrite(globalRole); !errors.As(err, &refusal) || !refusal.Deny {
		t.Fatalf("global role write = %v, want a hard HarnessRefusalError", err)
	}

	projectRole := filepath.Join(ws, ".agents", "roles", "x.md")
	if err := h.CheckWrite(projectRole); err == nil {
		t.Fatal("project role write with no approval surface must be refused")
	}

	var calls int
	h.Ask = func(path, reason string) bool { calls++; return true }
	if err := h.CheckWrite(projectRole); err != nil {
		t.Fatalf("approved project role write: %v", err)
	}
	if err := h.CheckWrite(projectRole); err != nil {
		t.Fatalf("second approved write: %v", err)
	}
	if calls != 1 {
		t.Errorf("approval asked %d times, want 1 (grant cached per path+reason)", calls)
	}

	h.Ask = func(path, reason string) bool { return false }
	other := filepath.Join(ws, ".agents", "roles", "y.md")
	if err := h.CheckWrite(other); err == nil {
		t.Fatal("declined project role write must be refused")
	}

	if err := h.CheckWrite(filepath.Join(ws, "src", "a.go")); err != nil {
		t.Fatalf("ordinary source write: %v", err)
	}
}

func TestHarnessPaths_WithWorkspaceRoot(t *testing.T) {
	home, _, ws, h := newGuardFixture(t)
	other := t.TempDir()

	derived := h.WithWorkspaceRoot(other)
	if got, _ := derived.Classify(filepath.Join(other, ".agents", "roles", "x.md")); got != HarnessAsk {
		t.Errorf("derived worktree role = %v, want ask", got)
	}
	if got, _ := derived.Classify(filepath.Join(ws, ".agents", "roles", "x.md")); got != HarnessAllow {
		t.Errorf("old workspace role = %v, want allow (roots are replaced)", got)
	}
	if got, _ := derived.Classify(filepath.Join(home, ".agents", "roles", "x.md")); got != HarnessDeny {
		t.Errorf("global role through derivation = %v, want deny", got)
	}
}

func TestHarnessPaths_CheckWrites(t *testing.T) {
	_, _, ws, h := newGuardFixture(t)
	blocked := h.CheckWrites(ws, []string{"src/a.go", ".agents/roles/x.md", ".yaah/log"})
	if len(blocked) != 2 || blocked[0] != ".agents/roles/x.md" || blocked[1] != ".yaah/log" {
		t.Fatalf("CheckWrites = %v, want the two harness paths in order", blocked)
	}
}

func TestPathValidator_ReadAllowedWriteGuarded(t *testing.T) {
	home, _, ws, h := newGuardFixture(t)
	// No containment: read access is not what this test is about.
	pv := NewPathValidator("", true, nil)
	pv.WorkDir = ws
	pv.Harness = h

	globalRole := filepath.Join(home, ".agents", "roles", "x.md")
	if _, err := pv.ResolvePath(globalRole); err != nil {
		t.Fatalf("reading a role file must stay allowed: %v", err)
	}
	if _, err := pv.ResolveWritePath(globalRole); err == nil {
		t.Fatal("writing a global role file must be refused")
	}

	pv.Harness.Ask = func(string, string) bool { return true }
	if _, err := pv.ResolveWritePath(filepath.Join(ws, ".agents", "roles", "x.md")); err != nil {
		t.Fatalf("approved project role write: %v", err)
	}
}

func TestPathValidator_WriteGuardResolvesSymlinksAndDotDot(t *testing.T) {
	_, _, ws, h := newGuardFixture(t)
	pv := NewPathValidator(ws, false, nil)
	pv.WorkDir = ws
	pv.Harness = h
	pv.Harness.Ask = func(string, string) bool { return true }

	if _, err := pv.ResolveWritePath("src/../.agents/roles/x.md"); err != nil {
		t.Fatalf("dot-dot path should still be guarded and approved: %v", err)
	}

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	roles := filepath.Join(ws, ".agents", "roles")
	if err := os.MkdirAll(roles, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(ws, "alias")
	if err := os.Symlink(roles, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	// Ask=false isolates the guard: if the alias resolved outside the
	// protected tree the write would be allowed without asking.
	pv.Harness.Ask = func(string, string) bool { return false }
	if _, err := pv.ResolveWritePath("alias/not-cached.md"); err == nil {
		t.Fatal("symlink into the roles directory must be guarded")
	}
}

func TestLocalWorkspace_WriteMethodsGuardHarnessPaths(t *testing.T) {
	_, _, ws, h := newGuardFixture(t)
	pv := NewPathValidator(ws, false, nil)
	pv.WorkDir = ws
	pv.Harness = h
	wsImpl := newLocalWorkspace(pv)

	role := filepath.Join(ws, ".agents", "roles", "x.md")
	if err := wsImpl.WriteFile(context.Background(), role, []byte("x"), 0o644); err == nil {
		t.Fatal("WriteFile into .agents/roles must be refused")
	}
	if err := wsImpl.Append(context.Background(), role, []byte("x"), 0o644); err == nil {
		t.Fatal("Append into .agents/roles must be refused")
	}
	if err := wsImpl.Remove(context.Background(), role); err == nil {
		t.Fatal("Remove of a role file must be refused")
	}
	if err := wsImpl.Move(context.Background(), role, filepath.Join(ws, "moved.md")); err == nil {
		t.Fatal("Move out of .agents/roles must be refused")
	}
	if err := wsImpl.Copy(context.Background(), filepath.Join(ws, "src.go"), role); err == nil {
		t.Fatal("Copy into .agents/roles must be refused")
	}

	src := filepath.Join(ws, "src", "a.go")
	if err := wsImpl.WriteFile(context.Background(), src, []byte("x"), 0o644); err != nil {
		t.Fatalf("ordinary source write: %v", err)
	}
}

func TestHarnessPaths_ClassifyResolvesSymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	// The rule roots are canonicalised, so a caller that passes the symlinked
	// form of the same directory (macOS /var → /private/var) must still match.
	real := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	h := NewHarnessPaths("", "", real)
	got, _ := h.Classify(filepath.Join(link, ".agents", "roles", "x.md"))
	if got != HarnessAsk {
		t.Fatalf("Classify through the symlinked root = %v, want ask", got)
	}
}

func TestHarnessPaths_CheckSandboxWritesRejectsEscapes(t *testing.T) {
	_, _, ws, h := newGuardFixture(t)
	blocked := h.CheckSandboxWrites(ws, "/workspace", []string{
		"/workspace/src/a.go",   // ordinary
		"/workspace/../evil.go", // escapes via dot-dot after the root prefix
		"../evil.go",            // relative escape
		"/workspace/.yaah",      // protected
	})
	if len(blocked) != 3 {
		t.Fatalf("CheckSandboxWrites = %v, want the two escapes and .yaah blocked", blocked)
	}
}
