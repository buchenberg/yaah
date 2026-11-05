package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/tools"
)

// subAgentToolSet is the full set of names a role may declare: the built-in
// leaf tools plus the names buildSubAgentRegistry wires itself (delegate and
// the memory trio). Session-only tools such as skill, plan, or todowrite are
// not registered for sub-agents, so a role that names one is asking for a tool
// that will be silently dropped.
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

// fileMutationTools are the tools that can change a file's contents or
// location. A role carrying any of them should also carry the directory pair
// (mkdir/rmdir) and the transfer pair (move/copy), or it can create a file but
// not tidy up or rename it after the fact.
var fileMutationTools = []string{"write", "edit", "patch", "replace", "delete", "move", "copy"}

var fileDirectoryTools = []string{"mkdir", "rmdir", "move", "copy"}

// TestRoleToolBelts_ValidAndComplete audits every built-in and project role:
// each declared tool must be one a sub-agent can actually receive, names must
// not repeat, and a role that mutates files must carry the full file-management
// set this repo ships (mkdir, rmdir, move, copy).
func TestRoleToolBelts_ValidAndComplete(t *testing.T) {
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(BuiltinRoleFiles()); err != nil {
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
			if seen[name] {
				t.Errorf("role %q declares %q more than once", role, name)
			}
			seen[name] = true
		}
		if !hasAnyTool(seen, fileMutationTools) {
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
