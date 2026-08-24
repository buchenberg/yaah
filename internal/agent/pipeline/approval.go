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
)

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
	approve func(name, args string) bool
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
		case GateAsk:
			// Origin policy asks independent of the global mode.
			if m.approve != nil && m.approve(name, args) {
				filtered = append(filtered, tc)
				continue
			}
			errMsg = fmt.Sprintf("error: tool %q was denied by user", name)
		case GateGlobal:
			switch m.mode {
			case "deny":
				errMsg = fmt.Sprintf("error: tool %q requires approval but approval mode is 'deny'", name)
			case "ask":
				if m.approve != nil && m.approve(name, args) {
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
