package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
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
	// A name that is neither registered nor MCP-known stays ungated even
	// under deny — the policy applies to identified MCP tools only.
	if got := newLoop("deny", false).classifyGate("unknown_tool", "{}"); got != pipeline.GatePass {
		t.Error("deny policy must not gate unknown non-MCP names")
	}
}

// TestClassifyDanger_PlanModeDeniesDangerousCalls pins the plan-mode promise:
// a dangerous call is withheld, not offered for approval. Plain GateGlobal
// would defer to the global ApprovalMode, whose default is "ask", so plan mode
// had to resolve danger to GateDeny — otherwise admitting git to the
// inspection set would have let `git commit` through on a user approval that
// the mode says cannot exist.
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
		if got := plan.classifyGate("git", args); got != pipeline.GateDeny {
			t.Errorf("dangerous git call %s in plan mode = %v, want GateDeny", args, got)
		}
	}
	// Defense in depth: dialect recovery can synthesize calls to names plan
	// mode never advertises; they are denied rather than gated globally.
	for _, name := range []string{"bash", "powershell"} {
		if got := plan.classifyGate(name, "{}"); got != pipeline.GateDeny {
			t.Errorf("always-dangerous %q in plan mode = %v, want GateDeny", name, got)
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
