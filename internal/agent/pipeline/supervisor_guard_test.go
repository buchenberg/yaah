package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func toolCall(id, name, args string) types.ToolCall {
	return types.ToolCall{ID: id, Type: "function", Function: types.ToolCallFn{Name: name, Arguments: args}}
}

// TestSupervisorGuard_DeniesBeforeDispatch pins the blocking contract
// (adoption plan B2): a denied tool call is stripped from the model's
// batch BEFORE anything dispatches — the tool never executes — and a
// synthesized result in the approval middleware's denied-call shape
// answers the tool_call_id, so the provider accepts the next request
// and the model reads an error surface it already knows.
func TestSupervisorGuard_DeniesBeforeDispatch(t *testing.T) {
	m := NewSupervisorGuardMiddleware(func(ownerID, toolName, argsJSON string) string {
		if toolName == "bash" {
			return "Blocked destructive operation (bash). Supervisor denied."
		}
		return ""
	}, "sub-worker")

	msg := &types.Message{ToolCalls: []types.ToolCall{
		toolCall("call-1", "bash", `{"command":"rm -rf /tmp/probe"}`),
		toolCall("call-2", "read", `{"path":"/workspace/file"}`),
	}}
	step := &Step{}
	step2, err := m.PostModel(context.Background(), msg, step)
	if err != nil {
		t.Fatalf("PostModel: %v", err)
	}
	if step2 == nil || step2.SynthesizedResults == nil {
		t.Fatal("a denial must synthesize a tool result")
	}

	// The dispatch reads msg.ToolCalls: stripping the denied call at
	// PostModel is what makes the deny pre-execution — the tool never
	// runs, so the tree is untouched.
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "read" {
		t.Errorf("remaining calls = %+v, want only the non-shell call to dispatch", msg.ToolCalls)
	}
	denied := step2.SynthesizedResults[0]
	if !strings.Contains(denied.Content, `tool "bash" denied by supervisor guard`) {
		t.Errorf("denied result = %q, want the approval middleware's denied-call shape naming the guard", denied.Content)
	}
	if !strings.Contains(denied.Content, "Supervisor denied") {
		t.Errorf("denied result = %q, want the kernel's deny reason carried through", denied.Content)
	}
}

// TestSupervisorGuard_NilCheckPassesAll pins the disabled shape: a nil
// check is the pass-through — tracing-off sessions dispatch
// byte-identical to today.
func TestSupervisorGuard_NilCheckPassesAll(t *testing.T) {
	m := NewSupervisorGuardMiddleware(nil, "sub-worker")
	msg := &types.Message{ToolCalls: []types.ToolCall{
		toolCall("call-1", "bash", `{"command":"rm -rf /tmp/probe"}`),
	}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatalf("PostModel: %v", err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Error("a nil check must leave the batch untouched")
	}
	if step.SynthesizedResults != nil {
		t.Error("a nil check must synthesize nothing")
	}
}

// TestSupervisorGuard_OwnerAddressing pins that the check addresses
// the loop's own scope: the guard's verdicts key per sub-agent, and a
// rule state must never leak across siblings.
func TestSupervisorGuard_OwnerAddressing(t *testing.T) {
	var seenOwner string
	m := NewSupervisorGuardMiddleware(func(ownerID, toolName, argsJSON string) string {
		seenOwner = ownerID
		return ""
	}, "sub-worker-sess-123")
	msg := &types.Message{ToolCalls: []types.ToolCall{
		toolCall("call-1", "read", `{"path":"/x"}`),
	}}
	if _, err := m.PostModel(context.Background(), msg, &Step{}); err != nil {
		t.Fatalf("PostModel: %v", err)
	}
	if seenOwner != "sub-worker-sess-123" {
		t.Errorf("check addressed %q, want the loop's trace owner", seenOwner)
	}
}

// TestNewSubAgentPipeline_GuardOrdering pins the assembly: the guard is
// installed only when the check is wired, third in the turn's path —
// after steer and permission, ahead of concurrency and tracing — so
// injected guidance and explicit rules see the turn first and nothing
// downstream observes a call the guard is about to deny.
func TestNewSubAgentPipeline_GuardOrdering(t *testing.T) {
	withGuard := NewSubAgentPipeline(PipelineConfig{
		DestructiveGuard: func(ownerID, toolName, argsJSON string) string { return "" },
	})
	var names []string
	for _, mw := range withGuard.middleware {
		names = append(names, mw.Name())
	}
	want := "supervisor_guard,tool_concurrency,shepherd_trace,evidence_guard"
	got := strings.Join(names, ",")
	if got != want {
		t.Errorf("pipeline = %q, want %q (no steer/permission wired in this config)", got, want)
	}

	withoutGuard := NewSubAgentPipeline(PipelineConfig{})
	for _, mw := range withoutGuard.middleware {
		if mw.Name() == "supervisor_guard" {
			t.Error("the guard must not install without a wired check")
		}
	}
}
