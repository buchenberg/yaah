package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// TestApprovalMiddlewareViaPipeline verifies end-to-end that the
// approval policy is enforced by the pipeline (not inline dispatch):
// dangerous calls are stripped from msg.ToolCalls and replaced by
// synthesized tool-result messages on step.SynthesizedResults.
func TestApprovalMiddlewareViaPipeline(t *testing.T) {
	l := &Loop{
		CtxMgr:   &ContextManager{},
		Registry: tools.NewRegistry(),
		Config:   LoopConfig{ApprovalMode: "deny"},
	}
	l.applyDefaults()
	pipe := l.buildPipeline()

	msg := types.AssistantMsg("", []types.ToolCall{
		{ID: "1", Function: types.ToolCallFn{Name: "bash", Arguments: `{"command":"ls"}`}},
		{ID: "2", Function: types.ToolCallFn{Name: "read", Arguments: `{"filePath":"/tmp/x"}`}},
	})
	step := &pipeline.Step{}
	if _, err := pipe.RunPostModel(context.Background(), &msg, step); err != nil {
		t.Fatal(err)
	}

	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "read" {
		t.Fatalf("expected only read to survive, got %+v", msg.ToolCalls)
	}
	if len(step.SynthesizedResults) != 1 {
		t.Fatalf("expected 1 synthesized result, got %d", len(step.SynthesizedResults))
	}
	synth := step.SynthesizedResults[0]
	if synth.ToolCallID != "1" || synth.Role != "tool" {
		t.Errorf("synthesized result = %+v, want tool message for call 1", synth)
	}
	if !strings.Contains(synth.Content, `requires approval but approval mode is 'deny'`) {
		t.Errorf("unexpected denial message: %q", synth.Content)
	}
}

// TestApprovalSubAgentLoopUnaffected ensures sub-agent loops keep
// ApprovalMode "allow" and exclude the approval middleware.
func TestApprovalSubAgentLoopUnaffected(t *testing.T) {
	names := pipeline.SubAgentPipelineNames(nil)
	if slices.Contains(names, "approval") {
		t.Error("sub-agent pipeline should not contain approval middleware")
	}
}
