package subagent

import (
	"log/slog"
	"slices"
)

// DispatchLane is the substrate/output pairing a delegate dispatch runs
// under (yaah-7yf): where the work runs, and what comes back.
type DispatchLane string

const (
	// LaneRetainedProposal runs the dispatch in an isolated worktree and
	// seals its delta into a proposal the orchestrator settles. The
	// historical lane, and the default for roles that do not declare an
	// output contract.
	LaneRetainedProposal DispatchLane = "retained-proposal"
	// LaneRetainedReport runs in an isolated worktree (scratch writes,
	// builds, and test runs stay off the parent tree) but returns the
	// sub-agent's report text directly; the worktree is torn down and no
	// proposal is created. For roles whose deliverable is a report.
	LaneRetainedReport DispatchLane = "retained-report"
	// LaneShared runs the dispatch directly on the shared workspace and
	// returns the report text. Only safe for roles that cannot mutate the
	// tree, so it is reachable only by a role that explicitly declares
	// output: report and carries no mutating tool.
	LaneShared DispatchLane = "shared"
)

// readOnlyWorkspaceTools is the whitelist of tools that cannot change
// the workspace. A role is treated as mutating when it carries anything
// else — including a tool added tomorrow that this list has not caught
// up with — so lane routing errs toward isolation. Memory and job
// tooling write to their own stores, not the workspace tree; go_test
// executes repo code (tests can rewrite golden files), so it stays off
// the whitelist.
var readOnlyWorkspaceTools = []string{
	"calculate",
	"diff",
	"file_info",
	"glob",
	"go_outline",
	"grep",
	"http",
	"json_query",
	"list_subagents",
	"ls",
	"memory_add",
	"memory_delete",
	"memory_search",
	"memory_search_sessions",
	"memory_update",
	"plan",
	"question",
	"read",
	"skill",
	"staticcheck",
	"subagent_jobs",
	"todowrite",
	"webfetch",
}

// normalizeRoleOutput validates the role file's output field. An unknown
// value warns and falls back to the default rather than failing the
// role: a broken project role must not brick startup (mirrors the budget
// clamps).
func normalizeRoleOutput(v, roleName string) string {
	switch v {
	case "":
		return ""
	case "report", "proposal":
		return v
	default:
		slog.Warn("role file: unknown output value; falling back to proposal", "role", roleName, "value", v)
		return ""
	}
}

// DispatchLaneFor reports how delegate should run the role. An explicit
// `output: proposal` always takes the retained lane. An explicit
// `output: report` is the decoupled cell: isolated-report when the role
// can mutate the tree (scratch writes stay off the parent, nothing is
// sealed), shared when it genuinely cannot (fresher data, no fork cost).
// A role that declares nothing keeps the historical retained-proposal
// behavior — the default never changes under an unmarked role file.
func DispatchLaneFor(role SubAgentRole) DispatchLane {
	p := RoleProfileFor(role)
	switch p.Output {
	case "report":
		if roleMutatesWorkspace(p.Tools) {
			return LaneRetainedReport
		}
		return LaneShared
	default:
		return LaneRetainedProposal
	}
}

// roleMutatesWorkspace reports whether the role's tool list can change
// the workspace. An empty list mutates nothing; any tool outside the
// read-only whitelist marks the role mutating.
func roleMutatesWorkspace(tools []string) bool {
	for _, t := range tools {
		if !slices.Contains(readOnlyWorkspaceTools, t) {
			return true
		}
	}
	return false
}
