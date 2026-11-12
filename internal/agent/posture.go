package agent

import "github.com/buchenberg/yaah/internal/permissions"

// approvalPosture is the derived approval configuration for one session: both
// postures the config exposes, decided once from the scalars, so the gate
// evaluator resolves a call against a settled value object instead of
// re-interpreting raw scalar config at each stage.
//
// Both halves live here because both resolve to the same vocabulary: an
// effect the ruleset ordering understands. The global `approval` posture
// governs dangerous built-in calls and the `mcp_approval` posture governs
// MCP-origin calls, but "the posture is a ceiling the ruleset may narrow and
// never widen" is one statement for both, expressed through
// permissions.Stricter.
//
// The posture is deliberately derived (not consulted live) so the evaluator's
// stages cannot drift apart about what a posture word means. The derivation is
// total: each field resolves its scalar in exactly one place, and the
// evaluator branches only on the resolved value.
type approvalPosture struct {
	// approval is the global `approval` posture: allow suppresses a veto but
	// never pre-approves a dangerous call, ask routes it through the approval
	// bridge, deny refuses it outright.
	approval permissions.Effect
	// mcp is the `mcp_approval` posture for MCP-origin calls. It is a ceiling
	// the ruleset may narrow but never widen, so its deny is final and its ask
	// stands under an allow rule.
	mcp permissions.Effect
}

// deriveApprovalPosture resolves the session's approval scalars into the value
// object the gate evaluator consumes. It is the single place that maps the raw
// `approval` and `mcp_approval` config scalars onto posture semantics.
func deriveApprovalPosture(cfg LoopConfig) approvalPosture {
	return approvalPosture{
		approval: postureEffect(cfg.ApprovalMode),
		mcp:      postureEffect(cfg.MCPApproval),
	}
}

// postureEffect maps a posture scalar onto the rule vocabulary. Unknown and
// unset values resolve to Ask, the conservative floor: a posture word the code
// does not recognize must never widen what the user configured. The config
// default is already applied at load, so at runtime only allow/ask/deny should
// arrive; "" is treated defensively and matches the pre-existing
// "ask on anything not allow/deny" resolution.
func postureEffect(raw string) permissions.Effect {
	switch raw {
	case "allow":
		return permissions.Allow
	case "deny":
		return permissions.Deny
	default: // "ask" and unset
		return permissions.Ask
	}
}

// approvalEffect is the global `approval` posture in the ruleset vocabulary. It
// applies to a dangerous built-in call and to nothing else: a safe call is
// never gated by it, and an MCP-origin call answers to mcpEffect instead.
func (p approvalPosture) approvalEffect() permissions.Effect { return p.approval }

// mcpEffect is the `mcp_approval` posture in the ruleset vocabulary. It applies
// to MCP-origin calls only; its deny is the ceiling no later rule widens.
func (p approvalPosture) mcpEffect() permissions.Effect { return p.mcp }

// posture derives the session's approval posture. Derived per call from
// the settled Config: the loop's Config is written only during wiring
// (options, builder), so the value is stable for the loop's lifetime
// while Loop literals in tests — which bypass the builder — still see a
// correct derivation.
func (l *Loop) posture() approvalPosture {
	return deriveApprovalPosture(l.Config)
}
