package subagent

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// RoleNotFoundError is returned when a sub-agent role name has no matching
// definition in the built-in or filesystem role registries. Use errors.Is with
// a zero value to match:
//
//	errors.Is(err, RoleNotFoundError{})
type RoleNotFoundError struct {
	Role string
}

func (e RoleNotFoundError) Error() string {
	return fmt.Sprintf("role %q not found", e.Role)
}

func (e RoleNotFoundError) Is(target error) bool {
	_, ok := target.(RoleNotFoundError)
	return ok
}

// SubAgentRole identifies the profile a sub-agent runs under. The role
// determines which tools the sub-agent has access to, its iteration
// budget, its default timeout, and how deeply it may nest further
// sub-agents. Every sub-agent runs under an explicit registered role;
// there is no default role.
type SubAgentRole string

// RoleProfile describes the capabilities and limits of a sub-agent
// role. Profiles are derived from parsed RoleDef entries in the
// default RoleRegistry installed at startup.
type RoleProfile struct {
	DisplayName   string
	Specialty     string
	Description   string
	Contract      ContractDef
	Tools         []string
	MaxLoopCycles int
	MaxToolTurns  int
	JSONMode      bool
	Timeout       time.Duration
}

// defaultRoleReg is set at startup by the CLI layer after built-in and
// user-defined role files have been loaded. When nil (e.g. in tests),
// every role resolves to the zero-value profile.
var defaultRoleReg atomic.Pointer[RoleRegistry]

// RoleDisplayName returns the human-facing name for a role. Falls back
// to the role identifier when no display name has been set.
func RoleDisplayName(role SubAgentRole) string {
	p := RoleProfileFor(role)
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return string(role)
}

// RoleSpecialty returns the specialty label for a role (e.g. "developer").
// Returns "" when no specialty is set.
func RoleSpecialty(role SubAgentRole) string {
	return RoleProfileFor(role).Specialty
}

// SetDefaultRoleRegistry installs the global registry used by
// RoleProfileFor and RoleGuidance. Call once at startup and again
// whenever roles are hot-reloaded from disk.
func SetDefaultRoleRegistry(r *RoleRegistry) {
	defaultRoleReg.Store(r)
}

// DefaultRegistry returns the current global role registry. May be nil
// before initialisation. Callers that enumerate roles at runtime should
// call this each time rather than caching a pointer.
func DefaultRegistry() *RoleRegistry {
	return defaultRoleReg.Load()
}

// RoleProfileFor returns the runtime profile for the given role.
// Unknown roles (and any role when no registry has been set) return
// the zero-value profile; callers treat that as a configuration error.
func RoleProfileFor(role SubAgentRole) RoleProfile {
	if r := defaultRoleReg.Load(); r != nil && len(r.Names()) > 0 {
		return r.ProfileFor(role)
	}
	return RoleProfile{}
}

// RoleGuidance returns system-prompt text appended to a sub-agent so
// it understands its role and constraints. Returns "" for unknown
// roles or when no registry has been set.
func RoleGuidance(role SubAgentRole) string {
	if r := defaultRoleReg.Load(); r != nil && len(r.Names()) > 0 {
		return r.Guidance(role)
	}
	return ""
}

// Roles returns the list of role names from the current default registry.
func Roles() []string {
	reg := defaultRoleReg.Load()
	if reg == nil {
		return nil
	}
	return reg.Names()
}

// ReloadDefaultRolesOptions holds the inputs needed to rebuild the
// default role registry from disk.
type ReloadDefaultRolesOptions struct {
	BuiltinFiles map[string][]byte
	SearchDirs   []string
}

// ReloadDefaultRoles rebuilds the default role registry from built-in
// files and on-disk directories, then atomically swaps it in. New
// sub-agent dispatches pick up the updated registry immediately;
// in-flight sub-agents continue with their already-loaded profiles.
func ReloadDefaultRoles(opts ReloadDefaultRolesOptions) error {
	reg := NewRoleRegistry()
	if err := reg.LoadBytes(opts.BuiltinFiles); err != nil {
		return err
	}
	for _, dir := range opts.SearchDirs {
		if err := reg.LoadDir(dir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
	}
	SetDefaultRoleRegistry(reg)
	return nil
}
