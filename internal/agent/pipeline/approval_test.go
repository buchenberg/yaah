package pipeline

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestApprovalMiddleware_DenyStripsDangerousCalls(t *testing.T) {
	var deniedArgs string
	m := &ApprovalMiddleware{
		authorize: func(name, args string) Decision {
			if name == "bash" {
				return Decision{Gate: GateDeny, Origin: OriginDanger}
			}
			return Decision{Gate: GatePass, Admitted: true}
		},
		approve: func(_ context.Context, _ string, name, args string) bool {
			t.Fatal("approve must not run for a terminal deny")
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
	// The global approval posture is now resolved by the evaluator, so the
	// middleware only reports the decision's origin — with the wording the
	// dispatch gate uses for the same verdict.
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

func TestApprovalMiddleware_AskHonoursApprove(t *testing.T) {
	calls := 0
	m := &ApprovalMiddleware{
		authorize: func(name, args string) Decision { return Decision{Gate: GateAsk} },
		approve:   func(_ context.Context, _ string, name, args string) bool { calls++; return true },
		emitDeny:  func(name, args, errMsg string) {},
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

func TestApprovalMiddleware_AskUserDenial(t *testing.T) {
	var emittedErr string
	m := &ApprovalMiddleware{
		authorize: func(name, args string) Decision { return Decision{Gate: GateAsk} },
		approve:   func(_ context.Context, _ string, name, args string) bool { return false },
		emitDeny:  func(name, args, errMsg string) { emittedErr = errMsg },
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

// TestApprovalMiddleware_PassKeepsCalls pins that the middleware holds no
// posture of its own: `approval: allow` reaches it as GatePass (the evaluator
// resolved the scalar), and a pass is never gated or prompted. The middleware
// no longer carries a mode field to disagree with the decision about.
func TestApprovalMiddleware_PassKeepsCalls(t *testing.T) {
	m := &ApprovalMiddleware{
		authorize: func(name, args string) Decision { return Decision{Gate: GatePass} },
		approve: func(_ context.Context, _ string, name, args string) bool {
			t.Fatal("approve must not run for a pass")
			return false
		},
		emitDeny: func(name, args, errMsg string) {},
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{{ID: "1", Function: types.ToolCallFn{Name: "bash"}}}}
	if _, err := m.PostModel(context.Background(), msg, &Step{}); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Error("a passing call must not be stripped")
	}
}

// TestApprovalMiddleware_MCPDenyIsFinal pins review finding S3: mcp_approval
// "deny" strips MCP tool calls whatever the global approval posture is — the
// per-origin policy cannot be downgraded by it, and the denial names the MCP
// posture rather than a rule the user wrote.
func TestApprovalMiddleware_MCPDenyIsFinal(t *testing.T) {
	m := &ApprovalMiddleware{
		authorize: func(name, args string) Decision {
			return Decision{Gate: GateDeny, Origin: OriginMCPPosture}
		},
		approve: func(_ context.Context, _ string, name, args string) bool {
			t.Fatal("GateDeny must not consult the user")
			return false
		},
		emitDeny: func(name, args, errMsg string) {},
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{{ID: "1", Function: types.ToolCallFn{Name: "github_create_issue"}}}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 0 {
		t.Error("GateDeny call survived")
	}
	if len(step.SynthesizedResults) != 1 || !contains(step.SynthesizedResults[0].Content, "mcp_approval 'deny'") {
		t.Errorf("synthesized results = %+v, want mcp_approval denial", step.SynthesizedResults)
	}
}

// TestApprovalMiddleware_MCPAskPrompts pins that mcp_approval "ask" prompts
// and honours the user's answer, one call at a time.
func TestApprovalMiddleware_MCPAskPrompts(t *testing.T) {
	asked := 0
	m := &ApprovalMiddleware{
		authorize: func(name, args string) Decision { return Decision{Gate: GateAsk} },
		approve:   func(_ context.Context, _ string, name, args string) bool { asked++; return asked == 1 },
		emitDeny:  func(name, args, errMsg string) {},
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Function: types.ToolCallFn{Name: "mcp_one"}},
		{ID: "2", Function: types.ToolCallFn{Name: "mcp_two"}},
	}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if asked != 2 {
		t.Errorf("approve invoked %d times, want 2 (GateAsk always prompts)", asked)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "mcp_one" {
		t.Errorf("surviving calls = %+v, want only the approved mcp_one", msg.ToolCalls)
	}
	if len(step.SynthesizedResults) != 1 || !contains(step.SynthesizedResults[0].Content, "denied by user") {
		t.Errorf("synthesized results = %+v, want one user-denial", step.SynthesizedResults)
	}
}

func TestApprovalMiddleware_NilAuthorizeIsInert(t *testing.T) {
	m := &ApprovalMiddleware{}
	msg := &types.Message{ToolCalls: []types.ToolCall{{ID: "1"}}}
	if _, err := m.PostModel(context.Background(), msg, &Step{}); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Error("inert middleware stripped a call")
	}
}

// TestApprovalMiddleware_WithheldNamesTheMode pins that a plan-mode denial
// does not claim an MCP origin policy caused it. The two are different
// promises, and the false cause sent callers to fix a policy they do not
// control (review finding).
func TestApprovalMiddleware_WithheldNamesTheMode(t *testing.T) {
	m := &ApprovalMiddleware{
		authorize: func(name, args string) Decision {
			return Decision{Gate: GateWithheld, Origin: OriginModeFloor}
		},
		approve: func(_ context.Context, _ string, name, args string) bool {
			t.Fatal("GateWithheld must not consult the user")
			return false
		},
		emitDeny: func(name, args, errMsg string) {},
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{{ID: "1", Function: types.ToolCallFn{Name: "git"}}}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 0 {
		t.Error("withheld call survived")
	}
	if len(step.SynthesizedResults) != 1 {
		t.Fatalf("expected 1 synthesized result, got %d", len(step.SynthesizedResults))
	}
	got := step.SynthesizedResults[0].Content
	if !contains(got, "withheld in plan mode") {
		t.Errorf("denial does not name plan mode: %q", got)
	}
	if contains(got, "mcp_approval") {
		t.Errorf("denial blames an MCP origin policy: %q", got)
	}
}

// TestApprovalMiddleware_RuleDenyNamesTheRule pins the reporting half of the
// single-decision refactor: GateDeny used to carry no origin, so every deny —
// including one the user wrote in `permissions:` — was reported as
// "denied by mcp_approval 'deny'", naming a config scalar the user may never
// have set. The middleware now takes the wording from the decision's origin.
func TestApprovalMiddleware_RuleDenyNamesTheRule(t *testing.T) {
	m := &ApprovalMiddleware{
		authorize: func(name, args string) Decision {
			return Decision{Gate: GateDeny, Origin: OriginRuleset, Reason: "permission rule: deny"}
		},
		approve: func(_ context.Context, _ string, name, args string) bool {
			t.Fatal("GateDeny must not consult the user")
			return false
		},
		emitDeny: func(name, args, errMsg string) {},
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{{ID: "1", Function: types.ToolCallFn{Name: "webfetch"}}}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 0 {
		t.Error("ruleset-denied call survived")
	}
	if len(step.SynthesizedResults) != 1 {
		t.Fatalf("expected 1 synthesized result, got %d", len(step.SynthesizedResults))
	}
	got := step.SynthesizedResults[0].Content
	if !contains(got, `denied by a permission rule`) {
		t.Errorf("denial does not name the rule: %q", got)
	}
	if contains(got, "mcp_approval") {
		t.Errorf("denial blames the mcp_approval posture for a user rule: %q", got)
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
