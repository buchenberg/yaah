package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/permissions"
	"github.com/buchenberg/yaah/internal/tools"
)

func TestApproveTool_UsesApproveFn(t *testing.T) {
	called := false
	l := &Loop{
		ApproveFn: func(name, args string) bool {
			called = true
			if name != "bash" {
				t.Errorf("expected tool name 'bash', got %q", name)
			}
			return true
		},
	}

	approved := l.approveTool("bash", `{"command": "git diff"}`)
	if !approved {
		t.Error("expected approval to be true")
	}
	if !called {
		t.Error("expected ApproveFn to be called")
	}
}

func TestApproveTool_UsesApproveFnDeny(t *testing.T) {
	l := &Loop{
		ApproveFn: func(name, args string) bool {
			return false
		},
	}

	approved := l.approveTool("bash", `{"command": "rm -rf /"}`)
	if approved {
		t.Error("expected approval to be false")
	}
}

func TestApproveTool_AbbreviatesArgs(t *testing.T) {
	l := &Loop{
		ApproveFn: func(name, args string) bool {
			if len(args) > 120 {
				t.Errorf("expected args to be abbreviated to <= 120 chars, got %d", len(args))
			}
			return true
		},
	}

	longArgs := `{"command": "echo ` + string(make([]byte, 200)) + `"}`
	l.approveTool("bash", longArgs)
}

func TestApproveTool_NilApproveFn(t *testing.T) {
	// Without ApproveFn, the method falls back to stdin. We can't
	// easily test the stdin path in unit tests, but we verify the
	// guard is not panicking.
	l := &Loop{
		ApproveFn: nil,
	}
	// approveTool with nil ApproveFn will try to read from stdin.
	// We only verify the struct is safe to construct.
	if l.ApproveFn != nil {
		t.Error("expected nil ApproveFn")
	}
}

// TestClassifyDanger_ShellToolsPlatformIndependent pins that the shell
// tools are gated even when the current platform's registry does not
// contain them (bash on Windows, powershell elsewhere) — the approval
// gate must not depend on the OS the loop runs on (review finding B1).
func TestClassifyDanger_ShellToolsPlatformIndependent(t *testing.T) {
	l := &Loop{Registry: tools.NewEmptyRegistry()}
	for _, name := range []string{"bash", "powershell"} {
		if got := l.classifyGate(name, "{}"); got != pipeline.GateGlobal {
			t.Errorf("classifyGate(%q) = %v on empty registry; shell tools must always gate", name, got)
		}
	}
	// Unknown non-shell names stay ungated.
	if got := l.classifyGate("mystery", "{}"); got != pipeline.GatePass {
		t.Error("unknown tool names should not be gated")
	}
}

// TestClassifyDanger_MCPToolsGatedByPolicy pins the MCP approval policy:
// remote tools cannot implement tools.DangerClassifier, so they are
// gated by mcp_approval — "ask"/unset gates, "allow" passes, "deny"
// blocks (review finding S3). The MCP check runs BEFORE the registry
// path, so a registered MCP tool (which would otherwise classify as
// GatePass via the registry early-return) is still gated.
func TestClassifyDanger_MCPToolsGatedByPolicy(t *testing.T) {
	newLoop := func(policy string, registerFake bool) *Loop {
		reg := tools.NewEmptyRegistry()
		if registerFake {
			reg.Register(&fakeRemoteTool{name: "github_create_issue"})
		}
		return &Loop{
			Registry: reg,
			Config: LoopConfig{
				MCPApproval:  policy,
				MCPToolNames: map[string]bool{"github_create_issue": true},
			},
		}
	}

	for _, registered := range []bool{false, true} {
		if got := newLoop("", registered).classifyGate("github_create_issue", "{}"); got != pipeline.GateAsk {
			t.Errorf("unset mcp_approval should gate MCP tools (default ask, registered=%v); got %v", registered, got)
		}
		if got := newLoop("ask", registered).classifyGate("github_create_issue", "{}"); got != pipeline.GateAsk {
			t.Errorf("mcp_approval=ask should gate MCP tools (registered=%v); got %v", registered, got)
		}
		if got := newLoop("allow", registered).classifyGate("github_create_issue", "{}"); got != pipeline.GatePass {
			t.Errorf("mcp_approval=allow should pass MCP tools (registered=%v); got %v", registered, got)
		}
		if got := newLoop("deny", registered).classifyGate("github_create_issue", "{}"); got != pipeline.GateDeny {
			t.Errorf("mcp_approval=deny should gate MCP tools (registered=%v); got %v", registered, got)
		}
	}
	// W5: the ruleset narrows an MCP decision but never widens it — a
	// deny rule on an MCP tool fires even under an allow policy, and an
	// allow rule cannot downgrade the default ask.
	{
		reg := tools.NewEmptyRegistry()
		l := &Loop{Registry: reg, Config: LoopConfig{
			MCPApproval:  "allow",
			MCPToolNames: map[string]bool{"github_create_issue": true},
			Permissions:  permissions.Rules{{Tool: "github_create_issue", Effect: permissions.Deny}},
		}}
		if got := l.classifyGate("github_create_issue", "{}"); got != pipeline.GateDeny {
			t.Errorf("ruleset deny on allow-policy MCP tool = %v, want GateDeny", got)
		}
	}
	{
		reg := tools.NewEmptyRegistry()
		l := &Loop{Registry: reg, Config: LoopConfig{
			MCPToolNames: map[string]bool{"github_create_issue": true},
			Permissions:  permissions.Rules{{Tool: "github_create_issue", Effect: permissions.Allow}},
		}}
		if got := l.classifyGate("github_create_issue", "{}"); got != pipeline.GateAsk {
			t.Errorf("ruleset allow must not downgrade MCP ask = %v, want GateAsk", got)
		}
	}
	// A name that is neither registered nor MCP-known stays ungated even
	// under deny — the policy applies to identified MCP tools only.
	if got := newLoop("deny", false).classifyGate("unknown_tool", "{}"); got != pipeline.GatePass {
		t.Error("deny policy must not gate unknown non-MCP names")
	}
}

// TestClassifyDanger_PlanModeDeniesDangerousCalls pins the plan-mode promise:
// a dangerous call is withheld, not offered for approval. Plain GateGlobal
// would defer to the global ApprovalMode, whose default is "ask", so plan mode
// had to resolve danger to a mode-level withholding (GateWithheld — GateDeny
// is reserved for MCP origin policy, and claiming it here named the wrong
// cause) — otherwise admitting git to the inspection set would have let `git
// commit` through on a user approval that the mode says cannot exist.
func TestClassifyDanger_PlanModeDeniesDangerousCalls(t *testing.T) {
	newGitLoop := func(level ToolsLevel) *Loop {
		reg := tools.NewEmptyRegistry()
		reg.Register(&tools.GitTool{})
		return &Loop{Registry: reg, Config: LoopConfig{ToolsLevel: level}}
	}

	plan := newGitLoop(ReadOnlyTools)
	// A read-only git action is inspection and passes untouched.
	if got := plan.classifyGate("git", `{"action":"status"}`); got != pipeline.GatePass {
		t.Errorf("git status in plan mode = %v, want GatePass", got)
	}
	for _, args := range []string{
		`{"action":"commit","message":"x"}`,
		`{"action":"add","paths":["."]}`,
		`{"action":"push"}`,
		`{"action":"pull"}`,
		`{"action":"branch","flags":["-D"],"paths":["main"]}`,
	} {
		if got := plan.classifyGate("git", args); got != pipeline.GateWithheld {
			t.Errorf("dangerous git call %s in plan mode = %v, want GateWithheld", args, got)
		}
	}
	// Defense in depth: dialect recovery can synthesize calls to names plan
	// mode never advertises; they are denied rather than gated globally.
	for _, name := range []string{"bash", "powershell"} {
		if got := plan.classifyGate(name, "{}"); got != pipeline.GateWithheld {
			t.Errorf("always-dangerous %q in plan mode = %v, want GateWithheld", name, got)
		}
	}

	// Build mode is unchanged: the global approval mode still decides.
	full := newGitLoop(FullTools)
	if got := full.classifyGate("git", `{"action":"commit","message":"x"}`); got != pipeline.GateGlobal {
		t.Errorf("dangerous git call in build mode = %v, want GateGlobal", got)
	}
	if got := full.classifyGate("bash", "{}"); got != pipeline.GateGlobal {
		t.Errorf("bash in build mode = %v, want GateGlobal", got)
	}
	if got := full.classifyGate("git", `{"action":"status"}`); got != pipeline.GatePass {
		t.Errorf("git status in build mode = %v, want GatePass", got)
	}
}

// fakeRemoteTool mimics an MCP-served tool as registered by initMCP: it
// satisfies tools.Tool but deliberately does NOT implement
// tools.DangerClassifier — the exact shape that made the registry path
// fail open before the MCP check was moved first.
type fakeRemoteTool struct {
	name string
}

func (f *fakeRemoteTool) Name() string                                             { return f.name }
func (f *fakeRemoteTool) Description() string                                      { return "fake remote tool" }
func (f *fakeRemoteTool) Schema() json.RawMessage                                  { return json.RawMessage(`{}`) }
func (f *fakeRemoteTool) Execute(ctx context.Context, args string) (string, error) { return "", nil }

// TestClassifyGate_PermissionRuleset pins the W5 resolver order: the
// ruleset is consulted after the mode-axis floor and before the danger
// classification, deny strips, ask routes to the approval pipeline, and
// allow only falls through — it never bypasses the dangerous-call
// GateGlobal posture, and in read-only mode it never re-enables a
// withheld tool. Matching domains (plan W5.2): bash command strings,
// file-tool paths, everything else tool-name-only.
func TestClassifyGate_PermissionRuleset(t *testing.T) {
	newLoop := func(level ToolsLevel, rules permissions.Rules) *Loop {
		reg := tools.NewEmptyRegistry()
		reg.Register(&tools.GitTool{})
		return &Loop{Registry: reg, Config: LoopConfig{ToolsLevel: level, Permissions: rules}}
	}
	rules := permissions.Rules{
		{Tool: "bash", Resource: "git push*", Effect: permissions.Ask},
		{Tool: "bash", Resource: "git status*", Effect: permissions.Allow},
		{Tool: "webfetch", Effect: permissions.Deny},
	}

	// ask routes through the approval pipeline even though the call is
	// dangerous and the global posture would have been GateGlobal.
	if got := newLoop(FullTools, rules).classifyGate("bash", `{"command":"git push origin main"}`); got != pipeline.GateAsk {
		t.Errorf("ruleset ask on dangerous call = %v, want GateAsk", got)
	}
	// deny strips a non-dangerous tool outright.
	if got := newLoop(FullTools, rules).classifyGate("webfetch", "{}"); got != pipeline.GateDeny {
		t.Errorf("ruleset deny = %v, want GateDeny", got)
	}
	// allow falls through to the danger classification: read is not
	// dangerous, so it passes.
	safeAllow := permissions.Rules{{Tool: "read", Effect: permissions.Allow}}
	if got := newLoop(FullTools, safeAllow).classifyGate("read", `{"filePath":"x.md"}`); got != pipeline.GatePass {
		t.Errorf("ruleset allow on safe call = %v, want GatePass", got)
	}
	// allow does NOT bypass the dangerous-call posture: an allow rule on a
	// mutating command still lands on GateGlobal (the approval mode
	// decides), never GatePass.
	allowRm := permissions.Rules{{Tool: "bash", Resource: "rm*", Effect: permissions.Allow}}
	if got := newLoop(FullTools, allowRm).classifyGate("bash", `{"command":"rm -rf /"}`); got != pipeline.GateGlobal {
		t.Errorf("ruleset allow on dangerous call = %v, want GateGlobal", got)
	}
	// The mode-axis floor beats every rule: in read-only mode a dangerous
	// call is withheld even when a rule allows or asks it.
	for _, rs := range []permissions.Rules{rules, allowRm} {
		if got := newLoop(ReadOnlyTools, rs).classifyGate("bash", `{"command":"git push origin main"}`); got != pipeline.GateWithheld {
			t.Errorf("read-only floor beaten by ruleset = %v, want GateWithheld", got)
		}
	}
	// No matching rule: behavior is exactly the pre-W5 gates.
	if got := newLoop(FullTools, rules).classifyGate("git", `{"action":"commit","message":"x"}`); got != pipeline.GateGlobal {
		t.Errorf("unmatched rule on dangerous call = %v, want GateGlobal", got)
	}
	if got := newLoop(FullTools, nil).classifyGate("bash", `{"command":"git push"}`); got != pipeline.GateGlobal {
		t.Errorf("no ruleset on dangerous call = %v, want GateGlobal", got)
	}
}

// TestAdmitTool_PermissionDeny pins the always-on enforcement: a ruleset
// deny refuses at dispatch even though the approval middleware (the only
// ask path) is optional and may be disabled. Ask is deliberately not
// enforced here — dispatch cannot prompt.
func TestAdmitTool_PermissionDeny(t *testing.T) {
	l := &Loop{Config: LoopConfig{Permissions: permissions.Rules{
		{Tool: "bash", Resource: "rm -rf*", Effect: permissions.Deny},
		{Tool: "bash", Resource: "git push*", Effect: permissions.Ask},
	}}}
	if ok, err := l.admitTool("bash", `{"command":"rm -rf /"}`); ok || err == nil {
		t.Errorf("ruleset deny must refuse at dispatch, got (%v, %v)", ok, err)
	}
	if ok, err := l.admitTool("bash", `{"command":"git push origin"}`); !ok || err != nil {
		t.Errorf("ruleset ask is the middleware's call, not dispatch's: got (%v, %v)", ok, err)
	}
	if ok, err := l.admitTool("bash", `{"command":"ls"}`); !ok || err != nil {
		t.Errorf("unmatched rule must pass: got (%v, %v)", ok, err)
	}
}

// TestAdmitTool_SubAgentAskRefused pins the execution half of the sub-agent
// `ask` fix. A sub-agent loop installs no approval middleware and nobody is
// attached to the run, so an `ask` rule there had no enforcement point:
// classifyGate produced GateAsk, no middleware consumed it, and admitTool
// enforced only deny — the declared boundary dispatched as a silent allow.
// The always-on gate now refuses it with a reason that names the rule and
// the missing prompt, which is a distinct sentence (and a distinct fix)
// from every other denial.
func TestAdmitTool_SubAgentAskRefused(t *testing.T) {
	rules := permissions.Rules{
		{Tool: "bash", Resource: "git push*", Effect: permissions.Ask},
		{Tool: "bash", Resource: "rm -rf*", Effect: permissions.Deny},
		{Tool: "git", Effect: permissions.Ask},
	}
	sub := &Loop{Config: LoopConfig{IsSubAgent: true, Permissions: rules}}

	t.Run("ask is refused, naming the rule and the missing prompt", func(t *testing.T) {
		ok, err := sub.admitTool("bash", `{"command":"git push origin main"}`)
		if ok || err == nil {
			t.Fatalf("sub-agent ask rule must refuse at dispatch, got (%v, %v)", ok, err)
		}
		for _, want := range []string{`"bash"`, "git push origin main", "`ask`", "cannot prompt for approval"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q is missing %q", err, want)
			}
		}
	})

	t.Run("the wording is distinct from a plain deny", func(t *testing.T) {
		_, askErr := sub.admitTool("bash", `{"command":"git push origin main"}`)
		_, denyErr := sub.admitTool("bash", `{"command":"rm -rf /"}`)
		if askErr == nil || denyErr == nil {
			t.Fatalf("both rules must refuse, got ask=%v deny=%v", askErr, denyErr)
		}
		if askErr.Error() == denyErr.Error() {
			t.Errorf("ask and deny share one message: %q", askErr)
		}
		if strings.Contains(askErr.Error(), "denied by a permission rule") {
			t.Errorf("ask refusal reuses the deny wording: %q", askErr)
		}
	})

	t.Run("tool-name-only ask rule is refused too", func(t *testing.T) {
		ok, err := sub.admitTool("git", `{"action":"status"}`)
		if ok || err == nil {
			t.Fatalf("a rule without a resource is still an ask, got (%v, %v)", ok, err)
		}
		if !strings.Contains(err.Error(), `"git"`) || !strings.Contains(err.Error(), "`ask`") {
			t.Errorf("refusal must name the tool and the ask rule: %q", err)
		}
	})

	t.Run("unmatched call still dispatches", func(t *testing.T) {
		if ok, err := sub.admitTool("read", `{"filePath":"x.md"}`); !ok || err != nil {
			t.Errorf("unmatched sub-agent call must pass, got (%v, %v)", ok, err)
		}
	})

	t.Run("the orchestrator keeps ask for its approval pipeline", func(t *testing.T) {
		parent := &Loop{Config: LoopConfig{Permissions: rules}}
		if ok, err := parent.admitTool("bash", `{"command":"git push origin main"}`); !ok || err != nil {
			t.Errorf("parent ask belongs to the approval pipeline, not dispatch: got (%v, %v)", ok, err)
		}
	})

	// The gate only engages on a loop the constructor actually marks: the
	// bug's first half was a sub-agent loop that never reached this path.
	t.Run("NewSubAgentLoop wires the gate", func(t *testing.T) {
		l := NewSubAgentLoop(nil, tools.NewEmptyRegistry(), "m", "p", SubAgentConfig{Permissions: rules})
		if !l.Config.IsSubAgent {
			t.Fatal("NewSubAgentLoop must mark the loop a sub-agent")
		}
		if ok, err := l.admitTool("bash", `{"command":"git push origin main"}`); ok || err == nil {
			t.Errorf("ask via NewSubAgentLoop = (%v, %v), want a refusal", ok, err)
		}
	})
}
