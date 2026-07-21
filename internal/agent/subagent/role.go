package subagent

import (
	"sync/atomic"
	"time"
)

// SubAgentRole identifies the profile a sub-agent runs under. The role
// determines which tools the sub-agent has access to, its iteration
// budget, its default timeout, and how deeply it may nest further
// sub-agents.
type SubAgentRole string

const (
	// RoleWorker can read, write, edit, delete, search, run shell
	// commands, and fetch URLs. It cannot spawn further sub-agents.
	RoleWorker SubAgentRole = "worker"

	// RoleReviewer is read-only: read, grep, glob, ls. Use it for code
	// review, analysis, and search-only tasks.
	RoleReviewer SubAgentRole = "reviewer"

	// RolePlanner inherits the worker tool set and additionally gains
	// the task tool, letting it decompose work and dispatch workers.
	RolePlanner SubAgentRole = "planner"

	// RoleDefault preserves the legacy task tool behaviour: the full
	// built-in tool set with no role-specific limits.
	RoleDefault SubAgentRole = ""
)

// RoleProfile describes the capabilities and limits of a sub-agent
// role. Profiles are derived from parsed RoleDef entries in the
// default RoleRegistry at startup; the fallback below provides
// built-in defaults when no registry has been set.
type RoleProfile struct {
	Tools         []string
	MaxIterations int
	Timeout       time.Duration
	MaxDepth      int
}

// IsSpawnCapable returns true if the role's profile includes the task
// tool and the role may therefore dispatch further sub-agents.
func (p RoleProfile) IsSpawnCapable() bool {
	for _, name := range p.Tools {
		if name == "task" {
			return true
		}
	}
	return false
}

// defaultRoleReg is set at startup by the CLI layer after built-in and
// user-defined role files have been loaded. When nil (e.g. in tests),
// the legacy built-in profiles are used as a fallback.
var defaultRoleReg atomic.Pointer[RoleRegistry]

// SetDefaultRoleRegistry installs the global registry used by
// RoleProfileFor and RoleGuidance. Call once at startup.
func SetDefaultRoleRegistry(r *RoleRegistry) {
	defaultRoleReg.Store(r)
}

// RoleProfileFor returns the runtime profile for the given role. Falls
// back to legacy built-in constants when no registry has been set.
func RoleProfileFor(role SubAgentRole) RoleProfile {
	if r := defaultRoleReg.Load(); r != nil {
		return r.ProfileFor(role)
	}
	return legacyProfileFor(role)
}

// RoleGuidance returns system-prompt text appended to a sub-agent so
// it understands its role and constraints. Returns "" for RoleDefault.
// Falls back to built-in text when no registry is set.
func RoleGuidance(role SubAgentRole) string {
	if r := defaultRoleReg.Load(); r != nil {
		return r.Guidance(role)
	}
	return legacyGuidance(role)
}

func legacyGuidance(role SubAgentRole) string {
	switch role {
	case RoleWorker:
		return "You are running as a WORKER sub-agent. Implement the assigned " +
			"task directly using the filesystem and shell tools available to " +
			"you. You cannot spawn further sub-agents. When you are done, " +
			"return a concise summary of what you did."
	case RoleReviewer:
		return "You are running as a REVIEWER sub-agent. You have read-only " +
			"tools. Analyze, review, or research the assigned topic and report " +
			"findings. Do not attempt to modify files."
	case RolePlanner:
		return "You are running as a PLANNER sub-agent. You may decompose the " +
			"work and dispatch WORKER sub-agents with the task tool for " +
			"parallel or isolated implementation. Coordinate their results " +
			"and return a consolidated summary."
	default:
		return ""
	}
}

// legacyProfileFor provides built-in hardcoded profiles so callers
// that never initialise a RoleRegistry (e.g. unit tests) still get
// sensible defaults.
func legacyProfileFor(role SubAgentRole) RoleProfile {
	switch role {
	case RoleWorker:
		return RoleProfile{
			Tools: []string{
				"read", "write", "edit", "delete",
				"grep", "glob", "ls",
				"bash", "powershell",
				"webfetch",
			},
			MaxIterations: 25,
			Timeout:       120 * time.Second,
			MaxDepth:      1,
		}
	case RoleReviewer:
		return RoleProfile{
			Tools:         []string{"read", "grep", "glob", "ls"},
			MaxIterations: 10,
			Timeout:       0,
			MaxDepth:      0,
		}
	case RolePlanner:
		return RoleProfile{
			Tools: []string{
				"read", "write", "edit", "delete",
				"grep", "glob", "ls",
				"bash", "powershell",
				"webfetch",
				"task",
			},
			MaxIterations: 50,
			Timeout:       300 * time.Second,
			MaxDepth:      3,
		}
	default:
		return RoleProfile{}
	}
}
