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
//     the admission axis for the tool level, and the gate axis for every
//     terminal verdict — plan mode's withholding and a posture or ruleset
//     deny — so none of them depends on the optional middleware running.
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
	// verdict: record it once, so the gate evaluator below reads the same
	// match instead of walking the ruleset twice. It stays the raw user-ruleset
	// fact even when a posture is what actually decided — provenance must not
	// claim a rule the user did not write.
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
// the call proceeds, prompts, or is stripped.
//
// It is ONE evaluator over two inputs — the posture the call's class derives
// from config and the user ruleset's effect on the call — and it runs in the
// order the parity plan (W5.3) pins:
//
//  1. The mode floor FIRST: in read-only mode a mutating call is withheld
//     before any policy is consulted. Nothing below can re-enable it — an
//     allow rule, an MCP posture, and the global approval posture all defer to
//     the mode's own promise, which is why this verdict is GateWithheld rather
//     than a deny that names a policy the user controls.
//  2. The posture effect: MCP-served tools are registered as *mcp.MCPTool
//     wrappers that cannot implement tools.DangerClassifier (they are remote),
//     so danger classification would clear them and the evaluator would fail
//     open. They therefore answer to the mcp_approval posture, which applies
//     regardless of registry presence and independently of the global approval
//     posture (review finding S3). A dangerous built-in call answers to the
//     global `approval` posture; a safe non-MCP call answers to neither, so no
//     posture can ever block it.
//  3. The user ruleset effect, resolved once in Authorize and recorded on the
//     decision, is the second operand.
//  4. The verdict is the STRICTER of the two, Deny > Ask > Allow
//     (permissions.Stricter). That one comparison is what the retired
//     per-origin branches were approximating: a rule narrows a posture and
//     never widens it, a posture deny stands under an allow rule, and
//     `approval: allow` passes a dangerous call only because the posture
//     itself is allow — an allow rule cannot produce that verdict ("allow
//     suppresses a veto; it does not pre-approve").
//
// Provenance follows the operand that decided: a deny/ask equal to the user
// rule's own effect is the ruleset's, otherwise it is the posture's (the MCP
// posture for an MCP-origin call, the global approval posture otherwise).
func (l *Loop) authorizeGate(name, args string, d *pipeline.Decision) {
	if l.Config.ToolsLevel == ReadOnlyTools && l.dangerousCall(name, args) {
		d.Gate = pipeline.GateWithheld
		d.Origin = pipeline.OriginModeFloor
		d.Reason = fmt.Sprintf("%q mutates and the read-only tool level withholds mutating calls", name)
		return
	}

	isMCP := l.Config.MCPToolNames[name]
	isDanger := l.dangerousCall(name, args)

	// The posture the call's class derives. A safe non-MCP call has none, and
	// "no posture" is Allow: nothing of the configuration's own making gates it.
	postureEffect := permissions.Allow
	switch {
	case isMCP:
		postureEffect = l.posture().mcpEffect()
	case isDanger:
		postureEffect = l.posture().approvalEffect()
	}

	// The user's own say. An unmatched call carries no constraint, which is
	// Allow and leaves the posture's verdict untouched.
	userEffect := permissions.Allow
	if d.RuleMatched {
		userEffect = d.RuleEffect
	}

	final := permissions.Stricter(postureEffect, userEffect)
	switch final {
	case permissions.Deny:
		d.Gate = pipeline.GateDeny
	case permissions.Ask:
		d.Gate = pipeline.GateAsk
		// A saved allow (yaah-x6vi.6) lifts an ask: the user answered
		// "always" for this (tool, resource) shape at an earlier prompt and
		// the row is persisted project-scoped. The consult fires only here
		// — the Ask case — so a deny from the ruleset or a posture stays
		// terminal (deny-before-saved), and the read-only floor above
		// returned long before anything could consult the store. The
		// origin is left at the stage that asked: the saved row narrows the
		// verdict, it does not become a new policy the user did not write.
		if l.Config.SavedAllow != nil && l.Config.SavedAllow(name, d.Resource) {
			d.Gate = pipeline.GatePass
		}
	default: // permissions.Allow: nothing narrowed the call.
		return
	}

	if d.RuleMatched && d.RuleEffect == final {
		d.Origin = pipeline.OriginRuleset
		if d.Resource == "" {
			d.Reason = fmt.Sprintf("permission rule: the last matching rule is %q for tool %q", final, name)
		} else {
			d.Reason = fmt.Sprintf("permission rule: the last matching rule is %q for %q", final, d.Resource)
		}
		return
	}
	if isMCP {
		d.Origin = pipeline.OriginMCPPosture
		if final == permissions.Deny {
			d.Reason = `mcp_approval "deny" denies MCP-origin calls and no rule can widen it`
		} else {
			d.Reason = `mcp_approval "ask" (or unset) gates MCP-origin calls`
		}
		return
	}
	d.Origin = pipeline.OriginDanger
	d.Reason = fmt.Sprintf("%q mutates, so the approval mode %q decides", name, final)
}

// admitTool is the always-on dispatch gate: the second consumer of the
// Authorize decision, and the one that cannot be switched off. The approval
// middleware runs the gate axis, but that middleware is optional
// (middleware.disabled: [approval]) and absent entirely from a sub-agent loop,
// so every terminal verdict must hold here too: a mode whose promise is
// "mutating actions are withheld", a posture that denies, and a rule that
// denies must not be switchable off by configuration. Dispatch adds the
// admission axis (the tool level, name-keyed) on top, so nothing it withholds
// is ever admitted while a gate-passed call can still be refused at the tool
// level.
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
	// Every terminal gate verdict is enforced here, not only in the approval
	// middleware, because dispatch is the layer that cannot be disabled.
	//
	// GateDeny is final whatever produced it: a user rule, the mcp_approval
	// posture, or the global approval posture being "deny". Leaving the posture
	// halves to the middleware made `mcp_approval: deny` a no-op whenever the
	// approval middleware was disabled or absent (every sub-agent loop), which
	// is the bug this gate closes. The wording comes from the decision's
	// origin, so a posture denial never reads as a rule the user wrote.
	//
	// GateAsk is deliberately NOT enforced here for an orchestrator loop —
	// dispatch has no way to prompt, and an ask that silently denied would
	// break the contract that asking is the user's choice; it requires the
	// approval pipeline (on by default). A SUB-AGENT loop is the opposite
	// case, and the reason is structural rather than a preference: its
	// pipeline never installs the approval middleware (see
	// pipeline.subAgentPipelineNames) and nobody is attached to the run, so an
	// ask has no enforcement point. Passing the call through turned a declared
	// "ask" boundary into a silent allow — the user's security intent, dropped
	// without a trace. Deny it instead, with the distinct reason that names the
	// boundary and the missing prompt.
	switch {
	case d.Gate == pipeline.GateDeny:
		return false, fmt.Errorf("%s", d.StripReason(name))
	case d.Gate == pipeline.GateAsk && l.Config.IsSubAgent:
		return false, fmt.Errorf("%s", pipeline.SubAgentAskReason(name, d.Resource))
	}
	return true, nil
}
