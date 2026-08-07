package tools

import (
	"errors"
	"fmt"
)

// RoleNotFoundError is returned when a sub-agent role name has no matching
// definition. It carries the offending role for diagnostics.
//
// Detect unknown-role errors with IsRoleNotFound(err) rather than
// errors.Is — this avoids a package-level sentinel error that would
// violate the "no globals" convention and ensures two different role
// names do not match each other.
type RoleNotFoundError struct {
	Role string
}

func (e RoleNotFoundError) Error() string {
	return fmt.Sprintf("role %q not found", e.Role)
}

// IsRoleNotFound reports whether err is a RoleNotFoundError (from either
// the tools or agent/subagent package). It uses errors.As so wrapped
// errors are detected.
func IsRoleNotFound(err error) bool {
	var rnfe RoleNotFoundError
	if errors.As(err, &rnfe) {
		return true
	}
	// The agent/subagent package has its own RoleNotFoundError type
	// with the same shape. Match it by error string prefix so tools
	// does not need to import agent/subagent.
	if err != nil && len(err.Error()) >= 5 && err.Error()[:5] == "role " {
		return true
	}
	return false
}

// ContractField names a field in a sub-agent's response contract and
// optionally classifies it as evidence (raw tool output, verifiable) or
// interpretation (model synthesis, may need verification).
//
// This is the tools-package-local copy of agent/subagent.ContractField.
// The RoleResolver.ListRoles method converts from the subagent type to
// this type so tools never imports agent/subagent.
type ContractField struct {
	Name string
	Kind string // "evidence" or "interpretation"; empty = interpretation
}

// RoleEntry describes a sub-agent role for the list_subagents and role
// tools. It is the tools-package-local representation; the RoleResolver
// converts from agent/subagent.RoleDef to this type.
type RoleEntry struct {
	Name        string
	DisplayName string
	Specialty   string
	Description string
	Contract    RoleContract
	Tools       []string
}

// RoleContract mirrors the YAML contract definition from role files.
type RoleContract struct {
	Heading string
	Fields  []ContractField
}

// RoleResolver provides role-related operations to the tools package
// without requiring tools to import agent/subagent. The agent/runner
// package injects the subagent-based implementation at wiring time.
//
// Methods:
//   - RoleNames: return all registered role names (for schema generation)
//   - ListRoles: return all role metadata (for list_subagents)
//   - ReloadRoles: rebuild the role registry from disk (for role tool mutations)
type RoleResolver interface {
	// RoleNames returns all registered role names in insertion order.
	RoleNames() []string

	// ListRoles returns metadata for all registered roles.
	ListRoles() []RoleEntry

	// ReloadRoles rebuilds the role registry from the given built-in
	// files and on-disk directories.
	ReloadRoles(builtinFiles map[string][]byte, searchDirs []string) error
}
