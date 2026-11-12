package agent

import (
	"testing"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/permissions"
	"github.com/buchenberg/yaah/internal/tools"
)

// TestPostureConformanceMatrix pins the evaluator's decision across every
// mcp_approval posture × tool class, per the bead-133 acceptance criteria.
// It must hold before and after the posture derivation refactor.
func TestPostureConformanceMatrix(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	loopFor := func(mcpApproval string, perms permissions.Rules) *Loop {
		return &Loop{
			Registry: reg,
			Config: LoopConfig{
				MCPApproval:  mcpApproval,
				MCPToolNames: map[string]bool{"mcp_remote": true},
				Permissions:  perms,
			},
		}
	}

	cases := []struct {
		name  string
		mcp   string
		perms permissions.Rules
		call  string // "mcp" | "safe"
		want  pipeline.GateDecision
	}{
		// MCP postures with no user rules.
		{"mcp-unset", "", nil, "mcp", pipeline.GateAsk},
		{"mcp-ask", "ask", nil, "mcp", pipeline.GateAsk},
		{"mcp-allow", "allow", nil, "mcp", pipeline.GatePass},
		{"mcp-deny", "deny", nil, "mcp", pipeline.GateDeny},

		// User deny narrows a ceiling policy (allow, ask, unset).
		{"mcp-allow-user-deny", "allow", permissions.Rules{{Tool: "mcp_remote", Effect: permissions.Deny}}, "mcp", pipeline.GateDeny},
		{"mcp-ask-user-deny", "ask", permissions.Rules{{Tool: "mcp_remote", Effect: permissions.Deny}}, "mcp", pipeline.GateDeny},
		{"mcp-unset-user-deny", "", permissions.Rules{{Tool: "mcp_remote", Effect: permissions.Deny}}, "mcp", pipeline.GateDeny},

		// User allow must never widen a policy floor (ask/unset stays ask,
		// deny stays deny — deny is final, no rule reaches it).
		{"mcp-ask-user-allow", "ask", permissions.Rules{{Tool: "mcp_remote", Effect: permissions.Allow}}, "mcp", pipeline.GateAsk},
		{"mcp-unset-user-allow", "", permissions.Rules{{Tool: "mcp_remote", Effect: permissions.Allow}}, "mcp", pipeline.GateAsk},
		{"mcp-deny-user-allow", "deny", permissions.Rules{{Tool: "mcp_remote", Effect: permissions.Allow}}, "mcp", pipeline.GateDeny},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := loopFor(tc.mcp, tc.perms)
			name := "safe_tool"
			if tc.call == "mcp" {
				name = "mcp_remote"
			}
			got := l.Authorize(name, "{}").Gate
			if got != tc.want {
				t.Errorf("mcp=%q perms=%v call=%q: got %v, want %v", tc.mcp, tc.perms, tc.call, got, tc.want)
			}
		})
	}
}
