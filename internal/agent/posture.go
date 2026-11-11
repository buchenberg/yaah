package agent

import "github.com/buchenberg/yaah/internal/agent/pipeline"

// approvalPosture is the derived approval configuration for one session:
// the posture decided once, from the config scalars, so the gate
// evaluator resolves calls against a settled value object instead of
// re-interpreting raw scalar config at each stage.
//
// The posture is deliberately derived (not consulted live) so every place
// that needs the same answers — classifyGate, the mcpGate ceiling walk,
// and the approval middleware's global stage — cannot drift apart about
// what a posture means. The derivation is total: each field resolves the
// scalar in exactly one place, and the evaluators branch only on the
// resolved value.
type approvalPosture struct {
	// allow: the mcp_approval posture passes MCP-origin calls that no
	// user rule narrows. allow is a ceiling for the ruleset, not a floor.
	mcpAllow bool
	// deny: the mcp_approval posture is final for MCP-origin calls —
	// no rule read later can widen it (review finding S3).
	mcpDeny bool
	// allow: the global approval posture passes dangerous calls
	// outright at the danger stage. ask stays the default; deny stays
	// a refusal naming the posture.
	globalAllow bool
}

// deriveApprovalPosture resolves the session's posture scalars into the
// value object the gate evaluator consumes. It is the single place that
// maps the raw `approval` and `mcp_approval` config scalars onto posture
// semantics; unset values mean the conservative floor (ask).
func deriveApprovalPosture(cfg LoopConfig) approvalPosture {
	return approvalPosture{
		mcpAllow:    cfg.MCPApproval == "allow",
		mcpDeny:     cfg.MCPApproval == "deny",
		globalAllow: cfg.ApprovalMode == "allow",
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
