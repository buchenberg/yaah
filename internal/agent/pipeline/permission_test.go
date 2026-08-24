package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// TestPermissionMiddleware_DeniedCallGetsSynthesizedResult guards
// against dangling tool_call_ids: a deny rule strips the call, so a
// synthesized tool result must be queued on step.SynthesizedResults or
// the provider rejects the next request.
func TestPermissionMiddleware_DeniedCallGetsSynthesizedResult(t *testing.T) {
	m := &PermissionMiddleware{rules: []PermissionRule{
		{Tool: "bash", Path: "/etc/*", Mode: "deny"},
	}}
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Function: types.ToolCallFn{Name: "bash", Arguments: `{"command":"cat /etc/passwd"}`}},
		{ID: "2", Function: types.ToolCallFn{Name: "bash", Arguments: `{"command":"ls /tmp"}`}},
	}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}

	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "2" {
		t.Fatalf("expected only call 2 to survive, got %+v", msg.ToolCalls)
	}
	if len(step.SynthesizedResults) != 1 {
		t.Fatalf("expected 1 synthesized result for denied call, got %d", len(step.SynthesizedResults))
	}
	synth := step.SynthesizedResults[0]
	if synth.ToolCallID != "1" || synth.Role != "tool" {
		t.Errorf("synthesized result = %+v, want tool message for call 1", synth)
	}
	if !strings.Contains(synth.Content, `denied by permission rule`) {
		t.Errorf("unexpected denial content: %q", synth.Content)
	}
}
