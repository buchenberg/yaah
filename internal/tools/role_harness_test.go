package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// role_harness_test.go pins the role tool's side of the harness-configuration
// guard: the tool writes role files directly (os.MkdirAll + atomicWriteFile),
// so it must consult the same policy the file tools use. Global roles are
// denied without a prompt; project roles require approval.
func TestRoleTool_HarnessGuard(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".yaah")
	ws := t.TempDir()

	globalDir := filepath.Join(home, ".agents", "roles")
	projectDir := filepath.Join(ws, ".agents", "roles")

	newTool := func(dir string, h *HarnessPaths) *RoleTool {
		return &RoleTool{Dirs: []string{dir}, Harness: h}
	}
	create := `{"action":"create","name":"auditor","description":"audits","body":"Audit."}`

	t.Run("global role is denied without creating its directory", func(t *testing.T) {
		h := NewHarnessPaths(home, configDir, ws)
		h.Ask = func(string, string) bool { return true } // a prompt must not help
		tool := newTool(globalDir, h)
		_, err := tool.Execute(context.Background(), create)
		var refusal *HarnessRefusalError
		if !errors.As(err, &refusal) || !refusal.Deny {
			t.Fatalf("create in ~/.agents/roles = %v, want a hard refusal", err)
		}
		if _, statErr := os.Stat(globalDir); !os.IsNotExist(statErr) {
			t.Errorf("a denied global create must not create %s", globalDir)
		}
	})

	t.Run("project role without an approval surface is refused", func(t *testing.T) {
		tool := newTool(projectDir, NewHarnessPaths(home, configDir, ws))
		if _, err := tool.Execute(context.Background(), create); err == nil {
			t.Fatal("create with no approval surface must be refused")
		}
	})

	t.Run("approved project create, then declined edit and delete", func(t *testing.T) {
		h := NewHarnessPaths(home, configDir, ws)
		h.Ask = func(string, string) bool { return true }
		tool := newTool(projectDir, h)
		if out, err := tool.Execute(context.Background(), create); err != nil {
			t.Fatalf("approved create: %v (output %s)", err, out)
		}

		// Fresh policy: the create's grant is cached per path+reason, so a
		// second decision needs a new session-scoped policy.
		h = NewHarnessPaths(home, configDir, ws)
		h.Ask = func(string, string) bool { return false }
		tool.Harness = h
		if _, err := tool.Execute(context.Background(), `{"action":"edit","name":"auditor","description":"changed"}`); err == nil {
			t.Fatal("declined edit must be refused")
		}
		if _, err := tool.Execute(context.Background(), `{"action":"delete","name":"auditor"}`); err == nil {
			t.Fatal("declined delete must be refused")
		}
		if _, err := os.Stat(filepath.Join(projectDir, "auditor.md")); err != nil {
			t.Fatalf("refused edit/delete must leave the role file in place: %v", err)
		}
	})
}
