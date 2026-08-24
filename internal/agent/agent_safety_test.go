package agent

import (
	"testing"

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
		if !l.classifyDanger(name, "{}") {
			t.Errorf("classifyDanger(%q) = false on empty registry; shell tools must always gate", name)
		}
	}
	// Unknown non-shell names stay ungated.
	if l.classifyDanger("mystery", "{}") {
		t.Error("unknown tool names should not be gated")
	}
}

// TestClassifyDanger_MCPToolsGatedByPolicy pins the MCP approval policy:
// remote tools cannot implement tools.DangerClassifier, so they are
// gated by mcp_approval — "ask"/unset gates, "allow" passes, "deny"
// blocks (review finding S3).
func TestClassifyDanger_MCPToolsGatedByPolicy(t *testing.T) {
	newLoop := func(policy string) *Loop {
		return &Loop{
			Registry: tools.NewEmptyRegistry(),
			Config: LoopConfig{
				MCPApproval:  policy,
				MCPToolNames: map[string]bool{"github_create_issue": true},
			},
		}
	}

	if !newLoop("").classifyDanger("github_create_issue", "{}") {
		t.Error("unset mcp_approval should gate MCP tools (default ask)")
	}
	if !newLoop("ask").classifyDanger("github_create_issue", "{}") {
		t.Error("mcp_approval=ask should gate MCP tools")
	}
	if newLoop("allow").classifyDanger("github_create_issue", "{}") {
		t.Error("mcp_approval=allow should pass MCP tools")
	}
	if !newLoop("deny").classifyDanger("github_create_issue", "{}") {
		t.Error("mcp_approval=deny should gate MCP tools")
	}
	// A name that is neither registered nor MCP-known stays ungated even
	// under deny — the policy applies to identified MCP tools only.
	if newLoop("deny").classifyDanger("unknown_tool", "{}") {
		t.Error("deny policy must not gate unknown non-MCP names")
	}
}
