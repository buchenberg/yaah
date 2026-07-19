package agent

import "time"

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

// RoleProfile describes the capabilities and limits of a sub-agent role.
type RoleProfile struct {
	// Tools is the ordered list of tool names the role may use.
	Tools []string

	// MaxIterations caps the sub-agent loop turns. 0 means inherit the
	// caller-supplied value or the Loop default.
	MaxIterations int

	// Timeout is the default wall-clock deadline for the role. 0 means
	// no timeout (the sub-agent runs until it finishes or the parent
	// context is cancelled).
	Timeout time.Duration

	// MaxDepth is the maximum number of nested sub-agent levels the
	// role may spawn. 0 means the role cannot nest further.
	MaxDepth int
}

// roleProfiles maps each role to its default profile. RoleDefault is
// intentionally absent — callers fall back to the legacy tool set.
var roleProfiles = map[SubAgentRole]RoleProfile{
	RoleWorker: {
		Tools: []string{
			"read", "write", "edit", "delete",
			"grep", "glob", "ls",
			"bash", "powershell",
			"webfetch",
		},
		MaxIterations: 25,
		Timeout:       120 * time.Second,
		MaxDepth:      1,
	},
	RoleReviewer: {
		Tools:         []string{"read", "grep", "glob", "ls"},
		MaxIterations: 10,
		Timeout:       0, // unlimited; reviewer tasks should not be cut off
		MaxDepth:      0,
	},
	RolePlanner: {
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
	},
}

// RoleProfileFor returns the profile for the given role. The RoleDefault
// role (empty string) returns the zero value, signalling the caller to
// use the legacy tool set.
func RoleProfileFor(role SubAgentRole) RoleProfile {
	if p, ok := roleProfiles[role]; ok {
		return p
	}
	return RoleProfile{}
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

// RoleGuidance returns system-prompt text appended to a sub-agent so it
// understands its role and constraints. Returns "" for RoleDefault.
func RoleGuidance(role SubAgentRole) string {
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
