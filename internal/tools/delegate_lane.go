package tools

import "github.com/buchenberg/yaah/internal/dispatch"

// DelegateLane selects the substrate/output pairing for one delegate
// dispatch (yaah-7yf): where the work runs, and what comes back. The
// wiring derives it from the role's declared output contract and tool
// set (subagent.DispatchLaneFor); a nil LaneForRole keeps every dispatch
// on the historical proposal lane.
//
// The vocabulary lives in internal/dispatch so the workflow executor and the
// tool share one definition; this alias keeps the tool's historical names.
type DelegateLane = dispatch.Lane

const (
	// LaneProposal: isolated world, sealed delta, orchestrator settles
	// with the proposal tool. The default.
	LaneProposal DelegateLane = dispatch.LaneProposal
	// LaneReport: isolated world, plain report result, world torn down
	// automatically — no proposal, nothing to settle.
	LaneReport DelegateLane = dispatch.LaneReport
	// LaneShared: runs directly on the shared workspace, plain report
	// result. Only routed for roles that cannot mutate the tree.
	LaneShared DelegateLane = dispatch.LaneShared
)
