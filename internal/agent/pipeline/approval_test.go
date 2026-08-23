package pipeline

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestApprovalMiddleware_DenyModeStripsDangerousCalls(t *testing.T) {
	var deniedArgs string
	m := &ApprovalMiddleware{
		mode: "deny",
		classify: func(name, args string) bool {
			return name == "bash"
		},
		approve: func(name, args string) bool {
			t.Fatal("approve must not run in deny mode")
			return false
		},
		emitDeny: func(name, args, errMsg string) { deniedArgs = args },
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Function: types.ToolCallFn{Name: "bash", Arguments: `{"command":"rm -rf /"}`}},
		{ID: "2", Function: types.ToolCallFn{Name: "read", Arguments: `{"filePath":"/etc/hosts"}`}},
	}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "read" {
		t.Errorf("dangerous call not stripped: %+v", msg.ToolCalls)
	}
	if len(step.SynthesizedResults) != 1 {
		t.Fatalf("expected 1 synthesized result, got %d", len(step.SynthesizedResults))
	}
	wantErr := `error: tool "bash" requires approval but approval mode is 'deny'`
	if !contains(step.SynthesizedResults[0].Content, wantErr) {
		t.Errorf("synthesized result = %q, want substring %q", step.SynthesizedResults[0].Content, wantErr)
	}
	if step.SynthesizedResults[0].ToolCallID != "1" || step.SynthesizedResults[0].Role != "tool" {
		t.Errorf("synthesized result not a tool message for call 1: %+v", step.SynthesizedResults[0])
	}
	if deniedArgs != `{"command":"rm -rf /"}` {
		t.Errorf("emitDeny not called with original args: %q", deniedArgs)
	}
}

func TestApprovalMiddleware_AskModeHonoursApprove(t *testing.T) {
	calls := 0
	m := &ApprovalMiddleware{
		mode:     "ask",
		classify: func(name, args string) bool { return true },
		approve:  func(name, args string) bool { calls++; return true },
		emitDeny: func(name, args, errMsg string) {},
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Function: types.ToolCallFn{Name: "write", Arguments: "{}"}},
	}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("approve invoked %d times, want 1", calls)
	}
	if len(msg.ToolCalls) != 1 {
		t.Error("approved call was stripped")
	}
	if len(step.SynthesizedResults) != 0 {
		t.Error("approved call produced a synthesized denial result")
	}
}

func TestApprovalMiddleware_AskModeUserDenial(t *testing.T) {
	var emittedErr string
	m := &ApprovalMiddleware{
		mode:     "ask",
		classify: func(name, args string) bool { return true },
		approve:  func(name, args string) bool { return false },
		emitDeny: func(name, args, errMsg string) { emittedErr = errMsg },
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Function: types.ToolCallFn{Name: "bash", Arguments: "{}"}},
	}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 0 {
		t.Error("denied call not stripped")
	}
	if len(step.SynthesizedResults) != 1 {
		t.Fatal("denied call produced no synthesized result")
	}
	if !contains(step.SynthesizedResults[0].Content, `was denied by user`) {
		t.Errorf("wrong denial message: %q", step.SynthesizedResults[0].Content)
	}
	if !contains(emittedErr, `was denied by user`) {
		t.Errorf("emitDeny got %q", emittedErr)
	}
}

func TestApprovalMiddleware_AllowModeIsNoop(t *testing.T) {
	called := false
	m := &ApprovalMiddleware{
		mode:     "allow",
		classify: func(name, args string) bool { called = true; return true },
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{{ID: "1"}}}
	if _, err := m.PostModel(context.Background(), msg, &Step{}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("classify invoked despite allow mode")
	}
	if len(msg.ToolCalls) != 1 {
		t.Error("allow mode stripped a call")
	}
}

func TestApprovalMiddleware_NilClassifyIsInert(t *testing.T) {
	m := &ApprovalMiddleware{mode: "deny"}
	msg := &types.Message{ToolCalls: []types.ToolCall{{ID: "1"}}}
	if _, err := m.PostModel(context.Background(), msg, &Step{}); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Error("inert middleware stripped a call")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
