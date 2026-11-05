package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/tools"
)

// subAgentToolSet is the full set of names a role may declare: the built-in
// leaf tools plus the names buildSubAgentRegistry wires itself. This validates
// names, not runtime availability: the memory tools are only registered when
// the dispatch has a DB, and memory_update only for the consolidator. The
// consolidator rule is enforced below because it is visible in the role file;
// the DB condition depends on the session and cannot be audited statically.
func subAgentToolSet() map[string]bool {
	known := map[string]bool{
		"delegate":      true,
		"memory_add":    true,
		"memory_search": true,
		"memory_update": true,
	}
	for _, name := range tools.LeafToolNames() {
		known[name] = true
	}
	return known
}

// leafFileMutationTools are the leaf tools that mutate a specific path. Shell
// and git are deliberately excluded: they can change files too, but the roles
// that carry them (analyst, reviewer, tester, checker, security_auditor) use
// them to inspect a repository and are read-only by role contract. This rule
// ensures a role given the leaf file tools can also manage the directories and
// transfers it creates; it is not a general "cannot mutate" proof.
var leafFileMutationTools = []string{"write", "edit", "patch", "replace", "delete", "move", "copy"}

var fileDirectoryTools = []string{"mkdir", "rmdir", "move", "copy"}

// TestRoleToolBelts_ValidAndComplete audits every built-in and project role:
// each declared name must be registerable (its runtime availability may still
// depend on the session), names must not repeat, memory_update is
// consolidator-only, and a role carrying a leaf file-mutation tool must carry
// the full file-management set this repo ships (mkdir, rmdir, move, copy).
func TestRoleToolBelts_ValidAndComplete(t *testing.T) {
	builtin := BuiltinRoleFiles()
	if len(builtin) == 0 {
		t.Fatal("no built-in role files embedded; the audit would skip half its scope")
	}
	for _, name := range []string{"analyst.md", "developer.md", "reviewer.md", "tester.md"} {
		if _, ok := builtin[name]; !ok {
			t.Errorf("built-in role %q is missing from the embedded set", name)
		}
	}

	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(builtin); err != nil {
		t.Fatalf("load built-in roles: %v", err)
	}
	projectRoles := filepath.Join("..", "..", "..", ".agents", "roles")
	if err := reg.LoadDir(projectRoles); err != nil {
		t.Fatalf("load project roles: %v", err)
	}

	roles := reg.List()
	if len(roles) == 0 {
		t.Fatal("no roles loaded; the audit would pass vacuously")
	}

	known := subAgentToolSet()
	for role, def := range roles {
		seen := make(map[string]bool, len(def.Tools))
		for _, name := range def.Tools {
			if !known[name] {
				t.Errorf("role %q declares %q, which no sub-agent registry can register", role, name)
			}
			if name == "memory_update" && def.DisplayName != "consolidator" {
				t.Errorf("role %q declares memory_update, which buildSubAgentRegistry registers only for the consolidator", role)
			}
			if seen[name] {
				t.Errorf("role %q declares %q more than once", role, name)
			}
			seen[name] = true
		}
		if !hasAnyTool(seen, leafFileMutationTools) {
			continue
		}
		for _, name := range fileDirectoryTools {
			if !seen[name] {
				t.Errorf("role %q mutates files but does not declare %q; add it to the role's tool belt", role, name)
			}
		}
	}
}

func hasAnyTool(seen map[string]bool, names []string) bool {
	for _, name := range names {
		if seen[name] {
			return true
		}
	}
	return false
}

// TestProjectRoleDirExists keeps the audit from silently skipping the project
// roles if someone moves the directory: the test above would still pass with
// only built-ins, hiding a whole role set from validation.
func TestProjectRoleDirExists(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "..", "..", ".agents", "roles")); err != nil {
		t.Fatalf("project role directory is missing: %v", err)
	}
}
