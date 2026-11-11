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

// TestBackgroundProcessStartReachesApprovalViaPipeline is the was9.8
// regression: background_process start (and restart) runs an arbitrary shell
// command, so it must resolve through the same approval gate as bash. The safe
// actions must stay ungated.
func TestBackgroundProcessStartReachesApprovalViaPipeline(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		args      string
		approve   bool
		wantCalls int
		wantKept  bool
	}{
		{"ask approves start", "ask", `{"action":"start","command":"echo hi"}`, true, 1, true},
		{"ask declines start", "ask", `{"action":"start","command":"echo hi"}`, false, 1, false},
		{"deny strips start", "deny", `{"action":"start","command":"echo hi"}`, false, 0, false},
		{"allow passes start", "allow", `{"action":"start","command":"echo hi"}`, false, 0, true},
		{"list stays ungated", "ask", `{"action":"list"}`, false, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewRegistry()
			reg.Register(&tools.BackgroundProcessTool{})
			l := &Loop{
				CtxMgr:   &ContextManager{},
				Registry: reg,
				Config:   LoopConfig{ApprovalMode: tc.mode},
			}
			calls := 0
			l.ApproveFn = func(name, args string) bool { calls++; return tc.approve }
			l.applyDefaults()
			pipe := l.buildPipeline()

			msg := types.AssistantMsg("", []types.ToolCall{
				{ID: "1", Function: types.ToolCallFn{Name: "background_process", Arguments: tc.args}},
			})
			step := &pipeline.Step{}
			if _, err := pipe.RunPostModel(context.Background(), &msg, step); err != nil {
				t.Fatal(err)
			}
			kept := len(msg.ToolCalls) == 1
			if kept != tc.wantKept {
				t.Errorf("call kept = %v, want %v (synthesized: %+v)", kept, tc.wantKept, step.SynthesizedResults)
			}
			if calls != tc.wantCalls {
				t.Errorf("approval calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}
