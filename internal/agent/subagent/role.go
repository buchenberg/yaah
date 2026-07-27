package subagent

import (
	"runtime"
	"sync/atomic"
	"time"
)

// platformShell returns the OS-appropriate shell tool name.
func platformShell() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "bash"
}

// SubAgentRole identifies the profile a sub-agent runs under. The role
// determines which tools the sub-agent has access to, its iteration
// budget, its default timeout, and how deeply it may nest further
// sub-agents.
type SubAgentRole string

const (
	// RoleDefault is the fallback role used when no sub-agent roles are
	// registered. It provides the full built-in tool set with no
	// role-specific limits and is only activated when the RoleRegistry
	// is empty.
	RoleDefault SubAgentRole = ""
)

// RoleProfile describes the capabilities and limits of a sub-agent
// role. Profiles are derived from parsed RoleDef entries in the
// default RoleRegistry at startup; the fallback below provides
// built-in defaults when no registry has been set.
type RoleProfile struct {
	DisplayName   string
	Specialty     string
	Description   string
	Contract      ContractDef
	Tools         []string
	MaxIterations int
	MaxTurns      int
	JSONMode      bool
	Timeout       time.Duration
}

// defaultRoleReg is set at startup by the CLI layer after built-in and
// user-defined role files have been loaded. When nil (e.g. in tests),
// the legacy built-in profiles are used as a fallback.
var defaultRoleReg atomic.Pointer[RoleRegistry]

// RoleDisplayName returns the human-facing name for a role. Falls back
// to the role identifier when no display name has been set.
func RoleDisplayName(role SubAgentRole) string {
	if role == RoleDefault {
		return "Pat"
	}
	p := RoleProfileFor(role)
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return string(role)
}

// RoleSpecialty returns the specialty label for a role (e.g. "developer").
// Returns "" when no specialty is set.
func RoleSpecialty(role SubAgentRole) string {
	if role == RoleDefault {
		return ""
	}
	return RoleProfileFor(role).Specialty
}

// SetDefaultRoleRegistry installs the global registry used by
// RoleProfileFor and RoleGuidance. Call once at startup.
func SetDefaultRoleRegistry(r *RoleRegistry) {
	defaultRoleReg.Store(r)
}

// RoleProfileFor returns the runtime profile for the given role. Falls
// back to the legacy built-in default profile when no registry has been
// set or when the registry is empty.
func RoleProfileFor(role SubAgentRole) RoleProfile {
	if r := defaultRoleReg.Load(); r != nil && len(r.Names()) > 0 {
		return r.ProfileFor(role)
	}
	return legacyProfileFor(role)
}

// RoleGuidance returns system-prompt text appended to a sub-agent so
// it understands its role and constraints. Returns "" for RoleDefault.
// Falls back to built-in text when no registry is set or it is empty.
func RoleGuidance(role SubAgentRole) string {
	if r := defaultRoleReg.Load(); r != nil && len(r.Names()) > 0 {
		return r.Guidance(role)
	}
	return legacyGuidance(role)
}

func legacyGuidance(role SubAgentRole) string {
	switch role {
	case RoleDefault:
		return "You are a sub-agent with full tool access. Complete the " +
			"assigned task and return a concise summary. Use the fewest " +
			"tools needed. Batch independent tool calls in one turn: fire " +
			"all reads, globs, greps, and go_outline calls at once instead " +
			"of one per turn. Report errors clearly if something cannot be done."
	default:
		return ""
	}
}

// legacyProfileFor provides built-in hardcoded profiles so callers
// that never initialise a RoleRegistry (e.g. unit tests) still get
// sensible defaults.
func legacyProfileFor(role SubAgentRole) RoleProfile {
	switch role {
	case RoleDefault:
		return RoleProfile{
			DisplayName: "Pat",
			Tools: []string{
				"read", "write", "edit", "delete", "replace",
				"json_query", "grep", "glob", "ls",
				platformShell(), "question", "webfetch",
				"git", "http", "go_outline", "calculate", "file_info",
				"spawn_subagent", "list_subagents", "todowrite",
				"skill", "plan", "background_process",
				"memory_search", "memory_add", "memory_delete",
				"memory_update", "memory_search_sessions",
			},
			MaxIterations: 25,
			MaxTurns:      3,
			Timeout:       180 * time.Second,
		}
	default:
		return RoleProfile{}
	}
}
