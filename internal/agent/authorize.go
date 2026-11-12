package agent

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/permissions"
)

// Authorize is the single authorization entry point for a tool call: it
// answers "may this call proceed, and how?" once, and both enforcement points
// consume its answer.
//
//   - The approval middleware (pipeline.ApprovalMiddleware, wired from
//     Loop.toPipelineConfig) consumes the gate axis: Gate, Origin, Reason.
//   - The always-on dispatch gate (admitTool) consumes the whole decision:
//     the admission axis for the tool level, the gate axis for plan mode's
//     withholding, and the recorded ruleset match for the deny/ask rules it
//     must enforce on its own.
//
// Neither consumer re-derives a policy: the ruleset is walked once here, the
// danger classification runs once here, and the posture is derived once per
// call. A name-keyed table (readOnlyToolNames, MCPToolNames, alwaysDangerous,
// the registry's DangerClassifiers, Config.PersonaTools) keeps its own job —
// only the decision path is shared.
//
// The result is total: every call maps to a Decision, refusals included. It
// returns no error because an authorization answer is a value, not a
// failure; the consumer that must synthesize a tool result owns its wording.
func (l *Loop) Authorize(name, args string) pipeline.Decision {
	d := pipeline.Decision{Gate: pipeline.GatePass, Admitted: true}

	// The ruleset resolution is a fact about the call rather than a stage
	// verdict: record it once, so the MCP ceiling below and the dispatch gate
	// read the same match instead of walking the ruleset twice.
	d.Resource = permissions.ResourceFor(name, args)
	if effect, matched := l.Config.Permissions.Match(name, d.Resource); matched {
		d.RuleMatched, d.RuleEffect = true, effect
	}

	if origin := l.nameAdmission(name); origin != pipeline.OriginNone {
		d.Admitted = false
		d.NarrowedBy = origin
	}

	l.authorizeGate(name, args, &d)
	return d
}

// nameAdmission reports the stage that excludes name from the advertised tool
// level, or OriginNone when the level admits it. The persona narrows first,
// the tool level second — the same order buildToolsForLevel builds the
// advertised defs in, so a synthesized call and the visible surface agree.
func (l *Loop) nameAdmission(name string) pipeline.RuleOrigin {
	if len(l.Config.PersonaTools) > 0 && !personaToolAllows(l.Config.PersonaTools, name) {
		return pipeline.OriginPersona
	}
	if l.Config.ToolsLevel == ReadOnlyTools && !readOnlyToolNames[name] {
		return pipeline.OriginToolLevel
	}
	return pipeline.OriginNone
}

// allowsTool enforces the advertised tool level at execution time. Hiding a
// tool definition is not enough: dialect recovery can synthesize calls the
// model never saw in the tool array.
//
// It gates on the name alone. A tool whose danger depends on its arguments
// (git) is let through here and refused per call by the gate axis of
// Authorize, which sees the arguments.
func (l *Loop) allowsTool(name string) bool {
	return l.nameAdmission(name) == pipeline.OriginNone
}

// authorizeGate fills in the gate axis of d: the stages that decide whether
// the call proceeds, prompts, or is stripped. They run in the order the
// parity plan (W5.3) pins, each able to narrow but none able to widen the one
// before it:
//
//  1. The mode floor FIRST: in read-only mode a mutating call is withheld
//     before any policy is consulted. Nothing below can re-enable it — an
//     allow rule, an MCP posture, and the global approval mode all defer to
//     the mode's own promise, which is why this is GateWithheld and not
//     GateDeny or GateGlobal.
//  2. The MCP origin policy NEXT: MCP-served tools are registered as
//     *mcp.MCPTool wrappers that cannot implement tools.DangerClassifier
//     (they are remote), so the registry path below would return GatePass for
//     them and fail open. The mcp_approval posture applies regardless of
//     registry presence and independently of the global approval mode
//     (review finding S3). The ruleset still narrows an MCP decision — a deny
//     rule naming an MCP tool fires — but it can never widen one: the
//     posture's deny is final and its ask stands even under an allow rule.
//  3. The permission ruleset: the last matching rule's verdict wins. deny
//     strips (GateDeny, not approvable), ask routes through the approval
//     pipeline (GateAsk), and allow only falls through — it suppresses
//     nothing and never bypasses the danger classification below, so "allow"
//     cannot sneak a dangerous call past the global approval posture.
//  4. The danger classification LAST: registered tools with a
//     DangerClassifier defer to it; alwaysDangerous names gate even when not
//     registered on this platform. The call then defers to the global
//     approval mode — GateGlobal is the only verdict left, since the mode
//     floor above already took the read-only case.
//
// Stages 1 and 2 run floor-then-posture, not the historical
// posture-then-floor, because the floor has to be absolute for both
// consumers of this decision. The only call the two orders disagree about is
// one that is MCP-origin AND mutating AND in read-only mode: a name collision
// between an MCP server's tool and a built-in inspection tool that classifies
// per call (git). The old split let the middleware report the posture while
// dispatch withheld the same call; the refusal and its wording were identical
// either way, so nothing observable changes except that one decision now
// stands for both layers.
func (l *Loop) authorizeGate(name, args string, d *pipeline.Decision) {
	if l.Config.ToolsLevel == ReadOnlyTools && l.dangerousCall(name, args) {
		d.Gate = pipeline.GateWithheld
		d.Origin = pipeline.OriginModeFloor
		d.Reason = fmt.Sprintf("%q mutates and the read-only tool level withholds mutating calls", name)
		return
	}
	if l.Config.MCPToolNames[name] {
		l.authorizeMCP(name, d)
		return
	}
	if applyRuleset(name, d) {
		return
	}
	if l.dangerousCall(name, args) {
		d.Gate = pipeline.GateGlobal
		d.Origin = pipeline.OriginDanger
		d.Reason = fmt.Sprintf("%q mutates, so the global approval mode decides", name)
	}
}

// authorizeMCP fills in the gate axis for a policy-gated MCP call. The
// mcp_approval posture is the ceiling for what the ruleset may narrow to: an
// allow posture falls through to the shared narrowing unchanged, a deny
// posture is final (no later rule can widen it), and the ask posture stands
// unless the ruleset tightens it. The shared narrowing is the same ruleset
// resolution the non-MCP path uses, so the two paths cannot disagree about
// what a matching rule means.
func (l *Loop) authorizeMCP(name string, d *pipeline.Decision) {
	p := l.posture()
	if p.mcpDeny {
		// The per-origin policy denies; no rule can widen it.
		d.Gate = pipeline.GateDeny
		d.Origin = pipeline.OriginMCPPosture
		d.Reason = `mcp_approval "deny" denies MCP-origin calls and no rule can widen it`
		return
	}
	if applyRuleset(name, d) {
		return
	}
	d.Gate = p.mcpDefault()
	d.Origin = pipeline.OriginMCPPosture
	if d.Gate == pipeline.GatePass {
		d.Reason = `mcp_approval "allow" passes MCP-origin calls no rule narrows`
	} else {
		d.Reason = `mcp_approval "ask" (or unset) gates MCP-origin calls`
	}
}

// applyRuleset resolves the call's recorded ruleset match into the decision,
// reporting whether it was final. deny and ask are final for this stage;
// allow and no-match fall through, which is the whole meaning of "allow
// suppresses nothing".
func applyRuleset(name string, d *pipeline.Decision) bool {
	if !d.RuleMatched {
		return false
	}
	switch d.RuleEffect {
	case permissions.Deny:
		d.Gate = pipeline.GateDeny
	case permissions.Ask:
		d.Gate = pipeline.GateAsk
	default: // permissions.Allow: fall through to the stage below.
		return false
	}
	d.Origin = pipeline.OriginRuleset
	if d.Resource == "" {
		d.Reason = fmt.Sprintf("permission rule: the last matching rule is %q for tool %q", d.RuleEffect, name)
	} else {
		d.Reason = fmt.Sprintf("permission rule: the last matching rule is %q for %q", d.RuleEffect, d.Resource)
	}
	return true
}

// admitTool is the always-on dispatch gate: the second consumer of the
// Authorize decision, and the one that cannot be switched off. The approval
// middleware runs the gate axis, but that middleware is optional
// (middleware.disabled: [approval]) and a mode whose promise is "mutating
// actions are withheld" must not be switchable off by configuration. Dispatch
// adds the admission axis (the tool level, name-keyed) on top, so nothing it
// withholds is ever admitted while a gate-passed call can still be refused at
// the tool level.
//
// The verdict is shared; the wording is per layer. This gate runs the
// admission axis first, so in read-only mode a name the level excludes is
// refused as "not available at the current tool level" even when the gate
// axis would have withheld the same call — alwaysDangerous makes `bash` that
// case. The gate axis reports GateWithheld with the withholding as its
// reason. Allow/deny agrees either way; only the message a user sees differs,
// which is why reordering these checks to align them would change what every
// excluded tool reports for no security gain.
func (l *Loop) admitTool(name, args string) (bool, error) {
	d := l.Authorize(name, args)
	if !d.Admitted {
		return false, fmt.Errorf("tool %q is not available at the current tool level", name)
	}
	if d.Withheld() {
		return false, fmt.Errorf("%s", pipeline.WithheldReason(name))
	}
	// Ruleset deny is enforced here as well as in the approval middleware
	// because that middleware is optional (middleware.disabled): a rule that
	// says deny must hold even when nothing can ask.
	//
	// Ruleset ask is deliberately NOT enforced here for an orchestrator loop
	// — dispatch has no way to prompt, and an ask that silently denied would
	// break the contract that asking is the user's choice; it requires the
	// approval pipeline (on by default). A SUB-AGENT loop is the opposite
	// case, and the reason is structural rather than a preference: its
	// pipeline never installs the approval middleware (see
	// pipeline.subAgentPipelineNames) and nobody is attached to the run, so
	// an ask rule has no enforcement point. Passing the call through turned a
	// declared "ask" boundary into a silent allow — the user's security
	// intent, dropped without a trace. Deny it instead, with the distinct
	// reason that names the rule and the missing prompt.
	switch {
	case d.RuleDenies():
		return false, fmt.Errorf("%s", pipeline.RuleDenyReason(name, d.Resource))
	case d.RuleAsks() && l.Config.IsSubAgent:
		return false, fmt.Errorf("%s", pipeline.SubAgentAskReason(name, d.Resource))
	}
	return true, nil
}
