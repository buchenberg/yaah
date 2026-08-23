package pipeline

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// ApprovalMiddleware enforces the tool approval policy (modes "deny" and
// "ask") before dispatch, mirroring the call-stripping behaviour of
// PermissionMiddleware. Classification, user prompting, and hook
// emission are injected as functions by the composition site
// (Loop.toPipelineConfig), keeping the pipeline→agent dependency
// direction clean.
//
// Dangerous calls are stripped from msg.ToolCalls and replaced by
// synthesized error tool-results on step.SynthesizedResults, preserving
// the provider invariant that every tool_call_id on an assistant message
// receives a result.
type ApprovalMiddleware struct {
	mode string
	// classify reports whether a call requires gating. When nil the
	// middleware is inert regardless of mode.
	classify func(name, args string) bool
	// approve asks the user (or ApproveFn); only consulted in ask mode.
	approve func(name, args string) bool
	// emitDeny fires the ToolStart/ToolEnd hook pair for a stripped call.
	emitDeny func(name, args, errMsg string)
}

func (m *ApprovalMiddleware) Name() string { return "approval" }

func (m *ApprovalMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *ApprovalMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	if m.classify == nil || (m.mode != "deny" && m.mode != "ask") {
		return step, nil
	}
	if len(msg.ToolCalls) == 0 {
		return step, nil
	}

	filtered := make([]types.ToolCall, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		name, args := tc.Function.Name, tc.Function.Arguments
		if !m.classify(name, args) {
			filtered = append(filtered, tc)
			continue
		}
		var errMsg string
		switch m.mode {
		case "deny":
			errMsg = fmt.Sprintf("error: tool %q requires approval but approval mode is 'deny'", name)
		case "ask":
			if m.approve(name, args) {
				filtered = append(filtered, tc)
				continue
			}
			errMsg = fmt.Sprintf("error: tool %q was denied by user", name)
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
