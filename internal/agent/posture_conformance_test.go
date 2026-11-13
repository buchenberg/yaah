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

// TestPostureMigrationMatrix is the acceptance matrix for folding the scalar
// `approval` and `mcp_approval` postures into the one evaluator (yaah-x6vi.5):
// every posture value crossed with every class of call that can answer to one.
//
// The point of the matrix is that the postures are no longer separate stages
// with their own branches. Both resolve to a permissions.Effect and are
// combined with the user ruleset by one ordering (Stricter: Deny > Ask >
// Allow), so the four invariants the refactor exists to establish are rows
// here, not prose:
//
//   - a safe call is never blocked by any posture (postures do not leak across
//     classes);
//   - a dangerous call is exactly `approval`: deny strips, ask prompts, allow
//     passes — allow is the only value that lets one through, and a user allow
//     rule cannot produce that (Stricter(Ask, Allow) is Ask);
//   - an MCP call is exactly `mcp_approval`, and its deny is final and its ask
//     stands: no later user allow rule widens either, while a user deny rule
//     still narrows an allow posture;
//   - the read-only floor wins over every posture.
//
// Every row also pins the dispatch verdict, because a posture enforced only
// by the approval middleware would not hold on a sub-agent loop (which never
// installs it), and older builds let the middleware be disabled outright.
// GateDeny is refused at dispatch whatever produced it.
func TestPostureMigrationMatrix(t *testing.T) {
	type row struct {
		name         string
		approval     string // global approval posture: "" (unset → ask) | ask | allow | deny
		mcpApproval  string
		tools        ToolsLevel
		tool         string
		args         string
		rules        permissions.Rules
		sub          bool // the loop is a sub-agent (no approval middleware)
		wantGate     pipeline.GateDecision
		wantOrigin   pipeline.RuleOrigin
		wantDispatch bool
	}

	rows := []row{
		// Safe call: neither posture reaches it, whatever they say.
		{name: "safe/approval-ask", approval: "ask", mcpApproval: "deny", tools: FullTools,
			tool: "read", args: readArgs,
			wantGate: pipeline.GatePass, wantOrigin: pipeline.OriginNone, wantDispatch: true},
		{name: "safe/approval-allow", approval: "allow", mcpApproval: "deny", tools: FullTools,
			tool: "read", args: readArgs,
			wantGate: pipeline.GatePass, wantOrigin: pipeline.OriginNone, wantDispatch: true},
		{name: "safe/approval-deny", approval: "deny", mcpApproval: "allow", tools: FullTools,
			tool: "read", args: readArgs,
			wantGate: pipeline.GatePass, wantOrigin: pipeline.OriginNone, wantDispatch: true},

		// Dangerous call: exactly the global approval posture.
		{name: "dangerous/approval-unset", tools: FullTools, tool: "bash", args: bashArgs,
			wantGate: pipeline.GateAsk, wantOrigin: pipeline.OriginDanger, wantDispatch: true},
		{name: "dangerous/approval-ask", approval: "ask", tools: FullTools, tool: "bash", args: bashArgs,
			wantGate: pipeline.GateAsk, wantOrigin: pipeline.OriginDanger, wantDispatch: true},
		{name: "dangerous/approval-allow", approval: "allow", tools: FullTools, tool: "bash", args: bashArgs,
			wantGate: pipeline.GatePass, wantOrigin: pipeline.OriginNone, wantDispatch: true},
		{name: "dangerous/approval-deny", approval: "deny", tools: FullTools, tool: "bash", args: bashArgs,
			wantGate: pipeline.GateDeny, wantOrigin: pipeline.OriginDanger, wantDispatch: false},
		// allow suppresses a veto; it does not pre-approve: under deny the
		// rule's allow is the weaker operand and changes nothing.
		{name: "dangerous/approval-deny-user-allow", approval: "deny", tools: FullTools, tool: "bash", args: bashArgs,
			rules:    permissions.Rules{{Tool: "bash", Effect: permissions.Allow}},
			wantGate: pipeline.GateDeny, wantOrigin: pipeline.OriginDanger, wantDispatch: false},
		// ... and under ask the rule's allow is still the weaker operand.
		{name: "dangerous/approval-ask-user-allow", approval: "ask", tools: FullTools, tool: "bash", args: bashArgs,
			rules:    permissions.Rules{{Tool: "bash", Effect: permissions.Allow}},
			wantGate: pipeline.GateAsk, wantOrigin: pipeline.OriginDanger, wantDispatch: true},
		// A user deny rule narrows an allow posture.
		{name: "dangerous/approval-allow-user-deny", approval: "allow", tools: FullTools, tool: "bash", args: bashArgs,
			rules:    permissions.Rules{{Tool: "bash", Effect: permissions.Deny}},
			wantGate: pipeline.GateDeny, wantOrigin: pipeline.OriginRuleset, wantDispatch: false},
		// A sub-agent loop has no approval bridge, so an ask it cannot honour
		// is refused rather than silently dispatched as an allow.
		{name: "dangerous/sub-ask-refused", approval: "ask", sub: true, tools: FullTools, tool: "bash", args: bashArgs,
			wantGate: pipeline.GateAsk, wantOrigin: pipeline.OriginDanger, wantDispatch: false},

		// MCP-origin call: exactly the mcp_approval posture, and it is a
		// ceiling — a rule narrows it, never widens it.
		{name: "mcp/mcp-allow", approval: "deny", mcpApproval: "allow", tools: FullTools, tool: mcpToolName, args: mcpArgs,
			wantGate: pipeline.GatePass, wantOrigin: pipeline.OriginNone, wantDispatch: true},
		{name: "mcp/mcp-ask", approval: "allow", mcpApproval: "ask", tools: FullTools, tool: mcpToolName, args: mcpArgs,
			wantGate: pipeline.GateAsk, wantOrigin: pipeline.OriginMCPPosture, wantDispatch: true},
		{name: "mcp/mcp-deny", approval: "allow", mcpApproval: "deny", tools: FullTools, tool: mcpToolName, args: mcpArgs,
			wantGate: pipeline.GateDeny, wantOrigin: pipeline.OriginMCPPosture, wantDispatch: false},
		// The dispatch half of the bug: mcp_approval deny with no matching
		// rule used to pass admitTool (the refusal lived only in the optional
		// middleware). It is now a real evaluator verdict, so dispatch refuses
		// it — and so does a sub-agent loop, which has no middleware at all.
		{name: "mcp/mcp-deny-sub", mcpApproval: "deny", sub: true, tools: FullTools, tool: mcpToolName, args: mcpArgs,
			wantGate: pipeline.GateDeny, wantOrigin: pipeline.OriginMCPPosture, wantDispatch: false},
		// ask stands under an allow rule (an orchestrator answers the prompt).
		{name: "mcp/mcp-ask-user-allow", mcpApproval: "ask", tools: FullTools, tool: mcpToolName, args: mcpArgs,
			rules:    permissions.Rules{{Tool: mcpToolName, Effect: permissions.Allow}},
			wantGate: pipeline.GateAsk, wantOrigin: pipeline.OriginMCPPosture, wantDispatch: true},
		// ... but a sub-agent cannot be asked, so there it is refused.
		{name: "mcp/mcp-ask-user-allow-sub", mcpApproval: "ask", sub: true, tools: FullTools, tool: mcpToolName, args: mcpArgs,
			rules:    permissions.Rules{{Tool: mcpToolName, Effect: permissions.Allow}},
			wantGate: pipeline.GateAsk, wantOrigin: pipeline.OriginMCPPosture, wantDispatch: false},
		// deny cannot be widened by a later allow rule.
		{name: "mcp/mcp-deny-user-allow", mcpApproval: "deny", tools: FullTools, tool: mcpToolName, args: mcpArgs,
			rules:    permissions.Rules{{Tool: mcpToolName, Effect: permissions.Allow}},
			wantGate: pipeline.GateDeny, wantOrigin: pipeline.OriginMCPPosture, wantDispatch: false},
		// A user deny rule narrows an allow posture.
		{name: "mcp/mcp-allow-user-deny", mcpApproval: "allow", tools: FullTools, tool: mcpToolName, args: mcpArgs,
			rules:    permissions.Rules{{Tool: mcpToolName, Effect: permissions.Deny}},
			wantGate: pipeline.GateDeny, wantOrigin: pipeline.OriginRuleset, wantDispatch: false},

		// Read-only floor: a mutating call is withheld, never offered for
		// approval, whatever either posture says.
		{name: "readonly/approval-ask", approval: "ask", mcpApproval: "ask", tools: ReadOnlyTools, tool: "git", args: gitCommitArgs,
			wantGate: pipeline.GateWithheld, wantOrigin: pipeline.OriginModeFloor, wantDispatch: false},
		{name: "readonly/approval-allow", approval: "allow", mcpApproval: "allow", tools: ReadOnlyTools, tool: "git", args: gitCommitArgs,
			wantGate: pipeline.GateWithheld, wantOrigin: pipeline.OriginModeFloor, wantDispatch: false},
		{name: "readonly/approval-deny", approval: "deny", mcpApproval: "deny", tools: ReadOnlyTools, tool: "git", args: gitCommitArgs,
			wantGate: pipeline.GateWithheld, wantOrigin: pipeline.OriginModeFloor, wantDispatch: false},
		// ... not even under an allow rule.
		{name: "readonly/approval-allow-user-allow", approval: "allow", tools: ReadOnlyTools, tool: "git", args: gitCommitArgs,
			rules:    permissions.Rules{{Tool: "git", Effect: permissions.Allow}},
			wantGate: pipeline.GateWithheld, wantOrigin: pipeline.OriginModeFloor, wantDispatch: false},
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewEmptyRegistry()
			// The real git tool is registered because its danger depends on
			// its arguments; the MCP wrapper deliberately cannot implement
			// DangerClassifier, which is why the MCP posture exists.
			reg.Register(&tools.GitTool{})
			reg.Register(&fakeRemoteTool{name: mcpToolName})
			l := &Loop{
				Registry: reg,
				Config: LoopConfig{
					ToolsLevel:   tc.tools,
					ApprovalMode: tc.approval,
					MCPApproval:  tc.mcpApproval,
					MCPToolNames: map[string]bool{mcpToolName: true},
					IsSubAgent:   tc.sub,
					Permissions:  tc.rules,
				},
			}

			d := l.Authorize(tc.tool, tc.args)
			if d.Gate != tc.wantGate {
				t.Fatalf("gate = %v, want %v (origin %v)", d.Gate, tc.wantGate, d.Origin)
			}
			if d.Origin != tc.wantOrigin {
				t.Errorf("origin = %v, want %v", d.Origin, tc.wantOrigin)
			}
			ok, err := l.admitTool(tc.tool, tc.args)
			if ok != tc.wantDispatch {
				t.Fatalf("admitTool = (%v, %v), want allowed=%v", ok, err, tc.wantDispatch)
			}
			if tc.wantDispatch && err != nil {
				t.Fatalf("admitted call carries an error: %v", err)
			}
			if !tc.wantDispatch && err == nil {
				t.Fatal("refused call carries no error")
			}
		})
	}
}
