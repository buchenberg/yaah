package agent

import "github.com/buchenberg/yaah/internal/agent/pipeline"

// approvalPosture is the derived MCP-origin approval configuration for one
// session: the posture decided once, from the config scalars, so the gate
// evaluator resolves MCP calls against a settled value object instead of
// re-interpreting raw scalar config at each stage.
//
// The posture is deliberately derived (not consulted live) so classifyGate
// and the mcpGate ceiling walk cannot drift apart about what an
// mcp_approval posture means. The derivation is total: each field resolves
// the scalar in exactly one place, and the evaluators branch only on the
// resolved value. The global approval posture is deliberately out of scope
// here: the approval middleware resolves that axis on its own
// (resolveApprovalMode), so it cannot consume this agent-level type without
// a dependency cycle.
type approvalPosture struct {
	// allow: the mcp_approval posture passes MCP-origin calls that no
	// user rule narrows. allow is a ceiling for the ruleset, not a floor.
	mcpAllow bool
	// deny: the mcp_approval posture is final for MCP-origin calls —
	// no rule read later can widen it (review finding S3).
	mcpDeny bool
}

// deriveApprovalPosture resolves the session's mcp_approval posture scalar
// into the value object the gate evaluator consumes. It is the single place
// that maps the raw `mcp_approval` config scalar onto posture semantics;
// unset values mean the conservative floor (ask).
func deriveApprovalPosture(cfg LoopConfig) approvalPosture {
	return approvalPosture{
		mcpAllow: cfg.MCPApproval == "allow",
		mcpDeny:  cfg.MCPApproval == "deny",
	}
}

// mcpDefault is the posture's verdict for an MCP-origin call that no user
// rule narrows: deny is final, allow passes, ask and unset ask.
func (p approvalPosture) mcpDefault() pipeline.GateDecision {
	if p.mcpDeny {
		return pipeline.GateDeny
	}
	if p.mcpAllow {
		return pipeline.GatePass
	}
	return pipeline.GateAsk
}
