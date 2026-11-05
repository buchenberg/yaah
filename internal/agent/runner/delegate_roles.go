package runner

import (
	"path/filepath"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/prompts"
)

// RoleDescriptionsFor maps each role name to a one-line description (or
// specialty fallback) drawn from the role registry. Used to populate the
// role parameter description of the delegate schema so the orchestrator
// can choose a role without calling list_subagents.
func RoleDescriptionsFor(roleNames []string) map[string]string {
	descs := make(map[string]string, len(roleNames))
	for _, name := range roleNames {
		p := subagent.RoleProfileFor(subagent.SubAgentRole(name))
		if p.Description != "" {
			descs[name] = p.Description
		} else if p.Specialty != "" {
			descs[name] = p.Specialty
		}
	}
	return descs
}

// BuiltinRoleFiles reads the embedded roles/*.md files shipped in the
// binary and returns them keyed by file name (e.g. "worker.md").
func BuiltinRoleFiles() map[string][]byte {
	entries, err := prompts.BuiltinRolesFS.ReadDir("roles")
	if err != nil {
		return nil
	}
	files := make(map[string][]byte, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, _ := prompts.BuiltinRolesFS.ReadFile("roles/" + e.Name())
		files[e.Name()] = data
	}
	return files
}

// RoleSearchPaths returns directories to scan for user-defined role
// definitions. Mirrors the skill search hierarchy: project-level
// (walked up from cwd) then user-level (~/.agents/roles/).
func RoleSearchPaths(cwd string) []string {
	home := config.HomeDir()
	var dirs []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, filepath.Join(dir, ".agents", "roles"))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	dirs = append(dirs, filepath.Join(home, ".agents", "roles"))
	return dirs
}
