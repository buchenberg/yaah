package pipeline

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/permissions"
)

// RuleOrigin names the policy stage that produced a Decision — or that
// excluded a call from the advertised tool level. It is the provenance half
// of an authorization decision: the stage that said no (or said ask), so a
// consumer can name the cause without re-deriving it, and so a trace can
// record it (decision provenance, yaah-x6vi.9).
//
// The value type lives here, next to GateDecision, because both consumers of
// the decision read it: the approval middleware in this package and the
// always-on dispatch gate in package agent. The policy tables themselves stay
// where they are — this type names a stage, it does not hold a policy.
//
// The zero value is OriginNone: "no stage decided". A Decision built by
// anything other than the loop's Authorize therefore reads as unattributed
// rather than as some arbitrary origin.
type RuleOrigin int

const (
	// OriginNone means no stage decided: the call passed every one of them
	// untouched.
	OriginNone RuleOrigin = iota
	// OriginPersona is the session persona's tools list, which narrows the
	// advertised surface (and the executable one) to the names it carries.
	OriginPersona
	// OriginToolLevel is the read-only tool level's inspection set, which
	// does not carry the tool.
	OriginToolLevel
	// OriginMCPPosture is the mcp_approval posture: the per-origin policy
	// for MCP-served tools, which the global approval mode cannot widen.
	OriginMCPPosture
	// OriginModeFloor is the mode floor: read-only mode withholds a
	// mutating call outright, no matter what any rule or posture says.
	OriginModeFloor
	// OriginRuleset is the user's permission ruleset, resolved
	// last-match-wins.
	OriginRuleset
	// OriginDanger is the danger classification, which defers to the global
	// approval mode: "ask" prompts, "deny" strips, "allow" passes.
	OriginDanger
)

// String renders the origin in the stable snake_case vocabulary config keys
// and decision provenance use. An out-of-range value renders "none": a
// decision must never claim a provenance the code cannot name.
func (o RuleOrigin) String() string {
	switch o {
	case OriginPersona:
		return "persona"
	case OriginToolLevel:
		return "tool_level"
	case OriginMCPPosture:
		return "mcp_posture"
	case OriginModeFloor:
		return "mode_floor"
	case OriginRuleset:
		return "ruleset"
	case OriginDanger:
		return "danger"
	default:
		return "none"
	}
}

// Decision is the single authorization verdict for one tool call: the answer
// to "may this call proceed, and how?" that both enforcement points consume.
//
// It answers two questions, which two different consumers need:
//
//   - The gate axis (Gate, Origin, Reason) is the per-call approval
//     classification. The approval middleware consumes it: a dangerous call
//     under an ask posture prompts, an origin policy's deny strips the call
//     even when the pipeline itself is the only enforcement point.
//   - The admission axis (Admitted, NarrowedBy) is the name-keyed tool-level
//     check. The always-on dispatch gate consumes it, because hiding a tool
//     definition is not enough: dialect recovery can synthesize calls the
//     model never saw advertised.
//
// The axes are reported separately on purpose. They are not one verdict
// order: the middleware must not consult admission (it classifies calls
// before dispatch, and plan mode's withholding has to be the gate decision
// there, not a tool-level refusal), while dispatch must consult both. Folding
// them into one enum would force one consumer to re-derive the other's
// answer, which is the split this type exists to remove.
type Decision struct {
	// Gate is the approval classification, one of the GateDecision values.
	Gate GateDecision
	// Origin names the stage that produced Gate. OriginNone when every
	// stage passed the call.
	Origin RuleOrigin
	// Reason explains the deciding stage in one sentence, for provenance
	// (traces, decision logs) rather than for the user-facing refusal —
	// each consumer words its own refusal so the surface it reports on
	// stays recognizable.
	Reason string
	// Admitted reports whether the advertised tool level carries this call's
	// name. False means dispatch must refuse it whatever Gate says.
	Admitted bool
	// NarrowedBy names the stage that excluded the name when Admitted is
	// false (OriginPersona or OriginToolLevel), OriginNone otherwise.
	NarrowedBy RuleOrigin
	// Resource is the call's permission resource (the shell command, the
	// target path, the URL, or "" for name-only matching), recorded once so
	// the gate stages and the dispatch gate cannot disagree about what the
	// call was matched against.
	Resource string
	// RuleMatched reports whether a permission rule matched the call, and
	// RuleEffect is the effect of the last matching rule. It is a recorded
	// fact about the call, not a stage verdict: a rule may match `allow`
	// and still leave the call to be gated as dangerous below, and dispatch
	// enforces a matching deny or ask even when the middleware that owns
	// the gate decision is disabled.
	RuleMatched bool
	RuleEffect  permissions.Effect
}

// Withheld reports whether the current mode withholds the call outright —
// the one gate decision that is neither approvable nor a policy the user
// controls.
func (d Decision) Withheld() bool { return d.Gate == GateWithheld }

// RuleDenies reports whether a permission rule denies the call.
func (d Decision) RuleDenies() bool {
	return d.RuleMatched && d.RuleEffect == permissions.Deny
}

// RuleAsks reports whether a permission rule asks for the call. It is the
// dispatch gate's concern only for a sub-agent loop, which has no approval
// bridge; an orchestrator loop hands ask to the approval pipeline.
func (d Decision) RuleAsks() bool {
	return d.RuleMatched && d.RuleEffect == permissions.Ask
}

// StripReason is the sentence a consumer reports for a terminal decision —
// GateDeny or GateWithheld, the two verdicts the user cannot approve past —
// chosen from the Origin so the report names the mechanism that actually
// applied. Before Origin existed the middleware had only the gate value and
// blamed mcp_approval for every deny, including a user's own deny rule.
//
// Any other gate returns "": a pass needs no explanation, and ask/global
// denials depend on the answer at the prompt, which no Decision can carry.
func (d Decision) StripReason(tool string) string {
	switch d.Gate {
	case GateDeny:
		if d.Origin == OriginMCPPosture {
			return MCPDenyReason(tool)
		}
		return RuleDenyReason(tool, d.Resource)
	case GateWithheld:
		return WithheldReason(tool)
	default:
		return ""
	}
}

// RuleDenyReason explains a denial caused by a user permission rule. Shared
// by the approval middleware and the dispatch gate so one rule reads the same
// whichever layer enforced it. A resource-scoped rule names the matched
// resource, mirroring SubAgentAskReason: the denial tells the user what
// matched, not just which tool was blocked.
func RuleDenyReason(tool, resource string) string {
	if resource == "" {
		return fmt.Sprintf("tool %q is denied by a permission rule", tool)
	}
	return fmt.Sprintf("tool %q is denied for %q by a permission rule", tool, resource)
}

// MCPDenyReason explains a denial caused by the mcp_approval posture. It is
// distinct from RuleDenyReason because the fix is different: the posture is a
// config scalar, while a rule lives in the ruleset (and may belong to the
// persona).
func MCPDenyReason(tool string) string {
	return fmt.Sprintf("tool %q is denied by mcp_approval 'deny'", tool)
}
