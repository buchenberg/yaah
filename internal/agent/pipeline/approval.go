package pipeline

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// GateDecision is the approval classification for a tool call. It
// decouples origin-specific policies (e.g. MCP tools) from the global
// ApprovalMode so the per-origin policy cannot be downgraded by it
// (review finding S3).
type GateDecision int

const (
	// GatePass never gates the call.
	GatePass GateDecision = iota
	// GateAsk always prompts the user, regardless of the global
	// approval mode (MCP "ask" policy).
	GateAsk
	// GateDeny always strips the call, regardless of the global
	// approval mode (MCP "deny" policy).
	GateDeny
	// GateGlobal defers to the global ApprovalMode: "ask" prompts,
	// "deny" strips, anything else passes (built-in dangerous tools).
	GateGlobal
	// GateWithheld denies the call because the current mode withholds it
	// (plan mode's inspection set), independent of the global approval
	// mode. It is a distinct decision from GateDeny so the denial names the
	// cause that actually applied: an MCP origin policy and a mode-level
	// withholding are different promises, and reporting the first when the
	// second happened sends the caller to fix a policy it does not control.
	GateWithheld
)

// WithheldReason explains a mode-level denial in the words the caller needs:
// what was refused, by which mechanism, and how the mode changes. Shared with
// the always-on dispatch gate (agent.admitTool) so the same refusal reads the
// same whether the optional approval middleware or dispatch produced it.
func WithheldReason(tool string) string {
	return fmt.Sprintf("tool %q is withheld in plan mode — mutating actions are not available; switch back to build mode to run it", tool)
}

// SubAgentAskReason explains a refusal produced by the always-on dispatch
// gate (agent.Loop.admitTool) when a sub-agent's ruleset says `ask`.
//
// It is deliberately distinct from every other denial, because each names a
// different fix: GateDeny points at a policy the user controls, GateWithheld
// at the mode, "denied by user" at the approval prompt, and this one at a
// boundary that has no enforcement point at all. A sub-agent dispatch runs
// with no approval middleware and nobody attached to it, so the question can
// never be put to anyone; letting the call through would have silently
// widened the role file's `ask` into `allow`, which is the bug this wording
// exists to make impossible to miss. The message names the rule and the
// missing prompt so the author's next move (declare `deny`, or run the call
// from the parent session) is in the text. Parity: Kilo's headless agents
// deny a sub-agent ask.
func SubAgentAskReason(tool, resource string) string {
	const tail = "and a sub-agent cannot prompt for approval — nobody is attached to a dispatch; declare the rule `deny` (or drop it) to make the intent explicit, or run this call from the parent session"
	if resource == "" {
		return fmt.Sprintf("tool %q is refused: its permission rule is `ask`, %s", tool, tail)
	}
	return fmt.Sprintf("tool %q is refused for %q: its permission rule is `ask`, %s", tool, resource, tail)
}

// ApprovalMiddleware enforces the tool approval policy before dispatch,
// mirroring the call-stripping behaviour of PermissionMiddleware.
// Classification, user prompting, and hook emission are injected as
// functions by the composition site (Loop.toPipelineConfig), keeping
// the pipeline→agent dependency direction clean.
//
// Dangerous calls are stripped from msg.ToolCalls and replaced by
// synthesized error tool-results on step.SynthesizedResults, preserving
// the provider invariant that every tool_call_id on an assistant message
// receives a result.
type ApprovalMiddleware struct {
	mode string
	// classify reports the gate decision for a call. When nil the
	// middleware is inert regardless of mode.
	classify func(name, args string) GateDecision
	// approve asks the user (or ApproveFn); consulted for GateAsk and
	// GateGlobal under "ask" mode.
	approve func(ctx context.Context, callID, name, args string) bool
	// emitDeny fires the ToolStart/ToolEnd hook pair for a stripped call.
	emitDeny func(name, args, errMsg string)
}

func (m *ApprovalMiddleware) Name() string { return "approval" }

func (m *ApprovalMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *ApprovalMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	if m.classify == nil {
		return step, nil
	}
	if len(msg.ToolCalls) == 0 {
		return step, nil
	}

	filtered := make([]types.ToolCall, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		name, args := tc.Function.Name, tc.Function.Arguments
		var errMsg string
		switch m.classify(name, args) {
		case GatePass:
			filtered = append(filtered, tc)
			continue
		case GateDeny:
			// Origin policy denies independent of the global mode —
			// the user cannot approve past it.
			errMsg = fmt.Sprintf("error: tool %q is denied by mcp_approval 'deny'", name)
		case GateWithheld:
			// The current mode withholds the call outright: a mode
			// decision, not an origin policy, and not approvable.
			errMsg = "error: " + WithheldReason(name)
		case GateAsk:
			// Origin policy asks independent of the global mode.
			if m.approve != nil && m.approve(ctx, tc.ID, name, args) {
				filtered = append(filtered, tc)
				continue
			}
			errMsg = fmt.Sprintf("error: tool %q was denied by user", name)
		case GateGlobal:
			switch m.mode {
			case "deny":
				errMsg = fmt.Sprintf("error: tool %q requires approval but approval mode is 'deny'", name)
			case "ask":
				if m.approve != nil && m.approve(ctx, tc.ID, name, args) {
					filtered = append(filtered, tc)
					continue
				}
				errMsg = fmt.Sprintf("error: tool %q was denied by user", name)
			default:
				// Global "allow"/unset: ungated.
				filtered = append(filtered, tc)
				continue
			}
		}
		m.emitDeny(name, args, errMsg)
		step.SynthesizedResults = append(step.SynthesizedResults,
			types.ToolResultMsg(tc.ID, name, errMsg))
	}
	msg.ToolCalls = filtered
	return step, nil
}

func (m *ApprovalMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
