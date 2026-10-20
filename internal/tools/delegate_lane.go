package tools

// DelegateLane selects the substrate/output pairing for one delegate
// dispatch (yaah-7yf): where the work runs, and what comes back. The
// wiring derives it from the role's declared output contract and tool
// set (subagent.DispatchLaneFor); a nil LaneForRole keeps every dispatch
// on the historical proposal lane.
type DelegateLane string

const (
	// LaneProposal: isolated worktree, sealed delta, orchestrator settles
	// with the proposal tool. The default.
	LaneProposal DelegateLane = "proposal"
	// LaneReport: isolated worktree, plain report result, worktree torn
	// down automatically — no proposal, nothing to settle.
	LaneReport DelegateLane = "report"
	// LaneShared: runs directly on the shared workspace, plain report
	// result. Only routed for roles that cannot mutate the tree.
	LaneShared DelegateLane = "shared"
)
