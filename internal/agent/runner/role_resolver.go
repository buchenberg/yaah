package runner

import (
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/tools"
)

// SubAgentRoleResolver adapts the global subagent.RoleRegistry to the
// tools.RoleResolver interface. This is the bridge that lets the tools
// package access role metadata without importing agent/subagent,
// breaking the design-level import cycle (tools → agent/subagent → ...).
type SubAgentRoleResolver struct{}

// Compile-time check: SubAgentRoleResolver satisfies tools.RoleResolver.
var _ tools.RoleResolver = SubAgentRoleResolver{}

// RoleNames returns all registered role names from the default registry.
func (SubAgentRoleResolver) RoleNames() []string {
	return subagent.Roles()
}

// ConvertContractFields translates subagent.ContractField values to the
// tools-package-local ContractField type. This is the single conversion
// site — both the ListSubAgentsTool wiring (cmd/yaah) and the
// SubAgentRoleResolver use this function to avoid drift.
func ConvertContractFields(fields []subagent.ContractField) []tools.ContractField {
	out := make([]tools.ContractField, len(fields))
	for i, f := range fields {
		out[i] = tools.ContractField{Name: f.Name, Kind: f.Kind}
	}
	return out
}

// ListRoles converts all registered role definitions from the subagent
// package format to the tools-package-local RoleEntry format.
func (SubAgentRoleResolver) ListRoles() []tools.RoleEntry {
	reg := subagent.DefaultRegistry()
	if reg == nil {
		return nil
	}
	entries := reg.List()
	out := make([]tools.RoleEntry, 0, len(entries))
	for role, def := range entries {
		out = append(out, tools.RoleEntry{
			Name:        string(role),
			DisplayName: def.DisplayName,
			Specialty:   def.Specialty,
			Description: def.Description,
			Contract: tools.RoleContract{
				Heading: def.Contract.Heading,
				Fields:  ConvertContractFields(def.Contract.Fields),
			},
			Tools: def.Tools,
		})
	}
	return out
}

// ReloadRoles rebuilds the default role registry from the given built-in
// files and on-disk directories.
func (SubAgentRoleResolver) ReloadRoles(builtinFiles map[string][]byte, searchDirs []string) error {
	return subagent.ReloadDefaultRoles(subagent.ReloadDefaultRolesOptions{
		BuiltinFiles: builtinFiles,
		SearchDirs:   searchDirs,
	})
}
