package yaah

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

// probeRoleContent is a minimal user-defined rolefile; the loader keys a
// role by its file name.
const probeRoleContent = `---
tools: [glob]
max_iterations: 2
timeout: 30
---
Probe role.`

// TestRebuildDefaultRoleRegistryUnionsSessionDirs pins the multi-session
// role-registry fix: a second session build with a different cwd must
// not *overwrite* the registry earlier sessions still dispatch against
// (the W in the kilo review) — the union of every contributed rolefile
// directory stays installed.
func TestRebuildDefaultRoleRegistryUnionsSessionDirs(t *testing.T) {
	savedReg := subagent.DefaultRegistry()
	rolefileDirsMu.Lock()
	savedDirs := rolefileDirs
	rolefileDirs = nil
	rolefileDirsMu.Unlock()
	t.Cleanup(func() {
		rolefileDirsMu.Lock()
		rolefileDirs = savedDirs
		rolefileDirsMu.Unlock()
		subagent.SetDefaultRoleRegistry(savedReg)
	})

	// Two sessions with different cwds, each carrying its own project
	// rolefile under <cwd>/.agents/roles (the path RoleSearchPaths
	// derives from a cwd).
	sessionDir := func(name string) string {
		t.Helper()
		root := t.TempDir()
		roles := filepath.Join(root, ".agents", "roles")
		if err := os.MkdirAll(roles, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(roles, name+".md"), []byte(probeRoleContent), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	hasRole := func(name string) bool {
		t.Helper()
		reg := subagent.DefaultRegistry()
		if reg == nil {
			t.Fatal("no default registry installed")
		}
		profile := subagent.RoleProfileFor(subagent.SubAgentRole(name))
		return len(profile.Tools) == 1 && profile.Tools[0] == "glob"
	}

	dirA := sessionDir("probe-a")
	rebuildDefaultRoleRegistry(dirA)
	if !hasRole("probe-a") {
		t.Fatal("session A's rolefile missing after its own build")
	}

	// Session B (different cwd) builds — session A's role must survive.
	dirB := sessionDir("probe-b")
	rebuildDefaultRoleRegistry(dirB)
	if !hasRole("probe-b") {
		t.Error("session B's rolefile missing after its build")
	}
	if !hasRole("probe-a") {
		t.Error("session B's build overwrote session A's rolefile — union broken")
	}
}
