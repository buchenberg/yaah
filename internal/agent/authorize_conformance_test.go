package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/permissions"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// The authorization conformance matrix (yaah-x6vi.4).
//
// Authorize is the one decision path both enforcement points consume, so its
// behaviour is pinned here as data: one row per combination of the axes that
// can change a verdict — the tool class, the tool level, the persona's tool
// list, the mcp_approval posture, the permission-rule effect and resource
// shape, and the loop's sub-agent flag. Every expectation below is the
// behaviour that held before the unification; the matrix exists so the next
// refactor of the decision path has to prove it changed nothing.
//
// The tool classes, each chosen to exercise a different policy table:
//
//   - inspection: "read", in the read-only inspection set, never dangerous.
//   - mutating-by-args: "git", in the inspection set by name but classified
//     per call by tools.GitTool (commit mutates, status does not).
//   - shell: "bash", alwaysDangerous and absent from the inspection set.
//   - MCP: "mcp_remote", in Config.MCPToolNames and registered as a remote
//     wrapper that cannot implement tools.DangerClassifier.
//   - unknown: "mystery", in no table at all.
const (
	readArgs      = `{"filePath":"x.md"}`
	gitCommitArgs = `{"action":"commit","message":"x"}`
	gitStatusArgs = `{"action":"status"}`
	bashArgs      = `{"command":"rm -rf /"}`
	mcpArgs       = `{"query":"x"}`
	unknownArgs   = `{}`

	mcpToolName = "mcp_remote"
)

// authorizeCase is one row of the matrix: the loop's configuration, the call,
// and the decision both consumers must reach.
type authorizeCase struct {
	name    string
	tools   ToolsLevel
	persona []string
	mcp     string // mcp_approval posture: "" (unset) | "ask" | "allow" | "deny"
	mcpTool bool   // the call's tool is MCP-served
	sub     bool   // the loop is a sub-agent (no approval middleware)
	rules   permissions.Rules
	tool    string
	args    string
	want    authorizeWant
}

// authorizeWant is the expected decision, expressed in the terms the callers
// see: the gate axis (what the approval middleware acts on), the admission
// axis (what dispatch acts on), the recorded ruleset match, and the dispatch
// verdict with the refusal wording it must carry.
type authorizeWant struct {
	gate        pipeline.GateDecision
	origin      pipeline.RuleOrigin
	admitted    bool
	narrowedBy  pipeline.RuleOrigin
	ruleMatched bool
	resource    string
	dispatch    bool
	refusal     string
}

// authorizeCases returns the matrix. Rules are written in both shapes the
// config accepts: name-only (every call of the tool) and resource-scoped
// (globbed against the call's resource — the shell command, the target path).
func authorizeCases() []authorizeCase {
	return []authorizeCase{
		// --- Inspection tool, nothing narrows it -------------------------
		{
			name: "inspection/full/no-rule", tools: FullTools,
			tool: "read", args: readArgs,
			want: authorizeWant{gate: pipeline.GatePass, origin: pipeline.OriginNone, admitted: true, resource: "x.md", dispatch: true},
		},
		{
			// allow is not a verdict: it suppresses the ruleset's own veto
			// and falls through, so the stage below (none here) decides.
			name: "inspection/full/rule-allow", tools: FullTools,
			rules: permissions.Rules{{Tool: "read", Effect: permissions.Allow}},
			tool:  "read", args: readArgs,
			want: authorizeWant{gate: pipeline.GatePass, origin: pipeline.OriginNone, admitted: true, ruleMatched: true, resource: "x.md", dispatch: true},
		},
		{
			// ask routes through the approval pipeline; dispatch leaves it
			// there for an orchestrator loop, which has a prompt to ask.
			name: "inspection/full/rule-ask-name-only", tools: FullTools,
			rules: permissions.Rules{{Tool: "read", Effect: permissions.Ask}},
			tool:  "read", args: readArgs,
			want: authorizeWant{gate: pipeline.GateAsk, origin: pipeline.OriginRuleset, admitted: true, ruleMatched: true, resource: "x.md", dispatch: true},
		},
		{
			name: "inspection/full/rule-deny-resource-match", tools: FullTools,
			rules: permissions.Rules{{Tool: "read", Resource: "*.md", Effect: permissions.Deny}},
			tool:  "read", args: readArgs,
			want: authorizeWant{gate: pipeline.GateDeny, origin: pipeline.OriginRuleset, admitted: true, ruleMatched: true, resource: "x.md", dispatch: false, refusal: "denied for \"x.md\" by a permission rule"},
		},
		{
			// A resource-scoped rule that does not match is no rule at all.
			name: "inspection/full/rule-deny-resource-miss", tools: FullTools,
			rules: permissions.Rules{{Tool: "read", Resource: "*.go", Effect: permissions.Deny}},
			tool:  "read", args: readArgs,
			want: authorizeWant{gate: pipeline.GatePass, origin: pipeline.OriginNone, admitted: true, resource: "x.md", dispatch: true},
		},
		{
			name: "inspection/readonly/no-rule", tools: ReadOnlyTools,
			tool: "read", args: readArgs,
			want: authorizeWant{gate: pipeline.GatePass, origin: pipeline.OriginNone, admitted: true, resource: "x.md", dispatch: true},
		},

		// --- Mutating-by-args tool (git) ---------------------------------
		{
			name: "git-commit/full/no-rule", tools: FullTools,
			tool: "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateGlobal, origin: pipeline.OriginDanger, admitted: true, dispatch: true},
		},
		{
			name: "git-commit/full/rule-ask", tools: FullTools,
			rules: permissions.Rules{{Tool: "git", Effect: permissions.Ask}},
			tool:  "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateAsk, origin: pipeline.OriginRuleset, admitted: true, ruleMatched: true, dispatch: true},
		},
		{
			name: "git-commit/full/rule-deny", tools: FullTools,
			rules: permissions.Rules{{Tool: "git", Effect: permissions.Deny}},
			tool:  "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateDeny, origin: pipeline.OriginRuleset, admitted: true, ruleMatched: true, dispatch: false, refusal: "denied by a permission rule"},
		},
		{
			// allow never bypasses the danger classification: a mutating
			// call still defers to the global approval mode.
			name: "git-commit/full/rule-allow-falls-through", tools: FullTools,
			rules: permissions.Rules{{Tool: "git", Effect: permissions.Allow}},
			tool:  "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateGlobal, origin: pipeline.OriginDanger, admitted: true, ruleMatched: true, dispatch: true},
		},
		{
			name: "git-commit/full/rule-other-tool", tools: FullTools,
			rules: permissions.Rules{{Tool: "bash", Effect: permissions.Deny}},
			tool:  "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateGlobal, origin: pipeline.OriginDanger, admitted: true, dispatch: true},
		},
		{
			// The mode floor: withheld, not offered for approval.
			name: "git-commit/readonly/no-rule", tools: ReadOnlyTools,
			tool: "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateWithheld, origin: pipeline.OriginModeFloor, admitted: true, dispatch: false, refusal: "withheld in plan mode"},
		},
		{
			// No rule re-enables a withheld tool.
			name: "git-commit/readonly/rule-allow", tools: ReadOnlyTools,
			rules: permissions.Rules{{Tool: "git", Effect: permissions.Allow}},
			tool:  "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateWithheld, origin: pipeline.OriginModeFloor, admitted: true, ruleMatched: true, dispatch: false, refusal: "withheld in plan mode"},
		},
		{
			name: "git-commit/readonly/rule-ask", tools: ReadOnlyTools,
			rules: permissions.Rules{{Tool: "git", Effect: permissions.Ask}},
			tool:  "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateWithheld, origin: pipeline.OriginModeFloor, admitted: true, ruleMatched: true, dispatch: false, refusal: "withheld in plan mode"},
		},
		{
			// The floor runs before the ruleset, so even a deny rule reports
			// the mode as the cause.
			name: "git-commit/readonly/rule-deny", tools: ReadOnlyTools,
			rules: permissions.Rules{{Tool: "git", Effect: permissions.Deny}},
			tool:  "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateWithheld, origin: pipeline.OriginModeFloor, admitted: true, ruleMatched: true, dispatch: false, refusal: "withheld in plan mode"},
		},
		{
			// git is in the inspection set by name; the per-call classifier
			// clears a read-only subcommand, so inspection survives.
			name: "git-status/readonly/no-rule", tools: ReadOnlyTools,
			tool: "git", args: gitStatusArgs,
			want: authorizeWant{gate: pipeline.GatePass, origin: pipeline.OriginNone, admitted: true, dispatch: true},
		},
		{
			name: "git-commit/full/persona-excludes", tools: FullTools,
			persona: []string{"read", "grep"},
			tool:    "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateGlobal, origin: pipeline.OriginDanger, admitted: false, narrowedBy: pipeline.OriginPersona, dispatch: false, refusal: "not available at the current tool level"},
		},
		{
			// The persona narrows first, so it is the named cause even though
			// the level would exclude the name too.
			name: "git-commit/readonly/persona-excludes", tools: ReadOnlyTools,
			persona: []string{"read"},
			tool:    "git", args: gitCommitArgs,
			want: authorizeWant{gate: pipeline.GateWithheld, origin: pipeline.OriginModeFloor, admitted: false, narrowedBy: pipeline.OriginPersona, dispatch: false, refusal: "not available at the current tool level"},
		},

		// --- Shell tool (alwaysDangerous, outside the inspection set) -----
		{
			name: "bash/full/no-rule", tools: FullTools,
			tool: "bash", args: bashArgs,
			want: authorizeWant{gate: pipeline.GateGlobal, origin: pipeline.OriginDanger, admitted: true, resource: "rm -rf /", dispatch: true},
		},
		{
			name: "bash/full/rule-deny-resource-match", tools: FullTools,
			rules: permissions.Rules{{Tool: "bash", Resource: "rm*", Effect: permissions.Deny}},
			tool:  "bash", args: bashArgs,
			want: authorizeWant{gate: pipeline.GateDeny, origin: pipeline.OriginRuleset, admitted: true, ruleMatched: true, resource: "rm -rf /", dispatch: false, refusal: "denied for \"rm -rf /\" by a permission rule"},
		},
		{
			name: "bash/full/rule-ask-resource-miss", tools: FullTools,
			rules: permissions.Rules{{Tool: "bash", Resource: "git push*", Effect: permissions.Ask}},
			tool:  "bash", args: bashArgs,
			want: authorizeWant{gate: pipeline.GateGlobal, origin: pipeline.OriginDanger, admitted: true, resource: "rm -rf /", dispatch: true},
		},
		{
			name: "bash/full/persona-names-it", tools: FullTools,
			persona: []string{"bash"},
			tool:    "bash", args: bashArgs,
			want: authorizeWant{gate: pipeline.GateGlobal, origin: pipeline.OriginDanger, admitted: true, resource: "rm -rf /", dispatch: true},
		},
		{
			name: "bash/full/persona-excludes", tools: FullTools,
			persona: []string{"read"},
			tool:    "bash", args: bashArgs,
			want: authorizeWant{gate: pipeline.GateGlobal, origin: pipeline.OriginDanger, admitted: false, narrowedBy: pipeline.OriginPersona, resource: "rm -rf /", dispatch: false, refusal: "not available at the current tool level"},
		},
		{
			// bash is outside the inspection set, so in read-only mode the
			// admission axis refuses it before the gate axis is consulted —
			// which is why the refusal is the tool-level one, not the
			// withholding (the two agree on the verdict, not on the wording).
			name: "bash/readonly/no-rule", tools: ReadOnlyTools,
			tool: "bash", args: bashArgs,
			want: authorizeWant{gate: pipeline.GateWithheld, origin: pipeline.OriginModeFloor, admitted: false, narrowedBy: pipeline.OriginToolLevel, resource: "rm -rf /", dispatch: false, refusal: "not available at the current tool level"},
		},
		{
			name: "bash/readonly/persona-names-it", tools: ReadOnlyTools,
			persona: []string{"bash"},
			tool:    "bash", args: bashArgs,
			want: authorizeWant{gate: pipeline.GateWithheld, origin: pipeline.OriginModeFloor, admitted: false, narrowedBy: pipeline.OriginToolLevel, resource: "rm -rf /", dispatch: false, refusal: "not available at the current tool level"},
		},

		// --- MCP-served tool ---------------------------------------------
		{
			name: "mcp/full/posture-unset", tools: FullTools, mcpTool: true,
			tool: mcpToolName, args: mcpArgs,
			want: authorizeWant{gate: pipeline.GateAsk, origin: pipeline.OriginMCPPosture, admitted: true, dispatch: true},
		},
		{
			name: "mcp/full/posture-ask", tools: FullTools, mcp: "ask", mcpTool: true,
			tool: mcpToolName, args: mcpArgs,
			want: authorizeWant{gate: pipeline.GateAsk, origin: pipeline.OriginMCPPosture, admitted: true, dispatch: true},
		},
		{
			name: "mcp/full/posture-allow", tools: FullTools, mcp: "allow", mcpTool: true,
			tool: mcpToolName, args: mcpArgs,
			want: authorizeWant{gate: pipeline.GatePass, origin: pipeline.OriginMCPPosture, admitted: true, dispatch: true},
		},
		{
			// The posture's deny is the middleware's call: dispatch enforces
			// the admission axis and the ruleset, and leaves an origin
			// policy to the approval pipeline (which is on by default).
			name: "mcp/full/posture-deny", tools: FullTools, mcp: "deny", mcpTool: true,
			tool: mcpToolName, args: mcpArgs,
			want: authorizeWant{gate: pipeline.GateDeny, origin: pipeline.OriginMCPPosture, admitted: true, dispatch: true},
		},
		{
			// A user deny rule narrows an allow posture.
			name: "mcp/full/posture-allow-rule-deny", tools: FullTools, mcp: "allow", mcpTool: true,
			rules: permissions.Rules{{Tool: mcpToolName, Effect: permissions.Deny}},
			tool:  mcpToolName, args: mcpArgs,
			want: authorizeWant{gate: pipeline.GateDeny, origin: pipeline.OriginRuleset, admitted: true, ruleMatched: true, dispatch: false, refusal: "denied by a permission rule"},
		},
		{
			// An allow rule cannot widen the ask floor.
			name: "mcp/full/posture-ask-rule-allow", tools: FullTools, mcp: "ask", mcpTool: true,
			rules: permissions.Rules{{Tool: mcpToolName, Effect: permissions.Allow}},
			tool:  mcpToolName, args: mcpArgs,
			want: authorizeWant{gate: pipeline.GateAsk, origin: pipeline.OriginMCPPosture, admitted: true, ruleMatched: true, dispatch: true},
		},
		{
			// ... nor re-enable a deny posture.
			name: "mcp/full/posture-deny-rule-allow", tools: FullTools, mcp: "deny", mcpTool: true,
			rules: permissions.Rules{{Tool: mcpToolName, Effect: permissions.Allow}},
			tool:  mcpToolName, args: mcpArgs,
			want: authorizeWant{gate: pipeline.GateDeny, origin: pipeline.OriginMCPPosture, admitted: true, ruleMatched: true, dispatch: true},
		},
		{
			// A user ask rule tightens an allow posture.
			name: "mcp/full/posture-allow-rule-ask", tools: FullTools, mcp: "allow", mcpTool: true,
			rules: permissions.Rules{{Tool: mcpToolName, Effect: permissions.Ask}},
			tool:  mcpToolName, args: mcpArgs,
			want: authorizeWant{gate: pipeline.GateAsk, origin: pipeline.OriginRuleset, admitted: true, ruleMatched: true, dispatch: true},
		},
		{
			// Read-only mode does not withhold a non-mutating remote tool,
			// but an MCP name is never in the inspection set, so the
			// admission axis refuses it at dispatch.
			name: "mcp/readonly/posture-ask", tools: ReadOnlyTools, mcp: "ask", mcpTool: true,
			tool: mcpToolName, args: mcpArgs,
			want: authorizeWant{gate: pipeline.GateAsk, origin: pipeline.OriginMCPPosture, admitted: false, narrowedBy: pipeline.OriginToolLevel, dispatch: false, refusal: "not available at the current tool level"},
		},
		{
			// A sub-agent has no approval bridge: an ask rule it cannot
			// honour is refused rather than silently widened to allow.
			name: "mcp/sub/rule-ask", tools: FullTools, sub: true, mcp: "allow", mcpTool: true,
			rules: permissions.Rules{{Tool: mcpToolName, Effect: permissions.Ask}},
			tool:  mcpToolName, args: mcpArgs,
			want: authorizeWant{gate: pipeline.GateAsk, origin: pipeline.OriginRuleset, admitted: true, ruleMatched: true, dispatch: false, refusal: "cannot prompt for approval"},
		},

		// --- Unknown tool -------------------------------------------------
		{
			name: "unknown/full/no-rule", tools: FullTools,
			tool: "mystery", args: unknownArgs,
			want: authorizeWant{gate: pipeline.GatePass, origin: pipeline.OriginNone, admitted: true, dispatch: true},
		},
		{
			// The gate axis knows nothing about a name no table carries, so
			// the middleware passes it; the admission axis refuses it. This
			// is the documented division of labour, not a hole: nothing
			// dispatches a call admitTool refuses.
			name: "unknown/readonly/no-rule", tools: ReadOnlyTools,
			tool: "mystery", args: unknownArgs,
			want: authorizeWant{gate: pipeline.GatePass, origin: pipeline.OriginNone, admitted: false, narrowedBy: pipeline.OriginToolLevel, dispatch: false, refusal: "not available at the current tool level"},
		},

		// --- Sub-agent ruleset enforcement -------------------------------
		{
			name: "bash/sub/rule-ask", tools: FullTools, sub: true,
			rules: permissions.Rules{{Tool: "bash", Resource: "git push*", Effect: permissions.Ask}},
			tool:  "bash", args: `{"command":"git push origin main"}`,
			want: authorizeWant{gate: pipeline.GateAsk, origin: pipeline.OriginRuleset, admitted: true, ruleMatched: true, resource: "git push origin main", dispatch: false, refusal: "cannot prompt for approval"},
		},
		{
			name: "bash/sub/rule-deny", tools: FullTools, sub: true,
			rules: permissions.Rules{{Tool: "bash", Effect: permissions.Deny}},
			tool:  "bash", args: bashArgs,
			want: authorizeWant{gate: pipeline.GateDeny, origin: pipeline.OriginRuleset, admitted: true, ruleMatched: true, resource: "rm -rf /", dispatch: false, refusal: "denied for \"rm -rf /\" by a permission rule"},
		},
	}
}

// newAuthorizeCaseLoop builds the loop a matrix row describes. The real git
// tool is registered because its danger depends on its arguments — without a
// DangerClassifier the alwaysDangerous fallback would clear every git call.
func newAuthorizeCaseLoop(c authorizeCase) *Loop {
	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.GitTool{})
	var mcpNames map[string]bool
	if c.mcpTool {
		// The remote wrapper cannot implement DangerClassifier, which is why
		// the MCP posture exists.
		reg.Register(&fakeRemoteTool{name: c.tool})
		mcpNames = map[string]bool{c.tool: true}
	}
	return &Loop{
		Registry: reg,
		Config: LoopConfig{
			ToolsLevel:   c.tools,
			PersonaTools: c.persona,
			MCPApproval:  c.mcp,
			MCPToolNames: mcpNames,
			IsSubAgent:   c.sub,
			Permissions:  c.rules,
		},
	}
}

// TestAuthorizeDecisionMatrix pins the decision path across the cross-product
// of tool class, tool level, persona list, MCP posture, rule effect and
// resource shape, and pins that the always-on dispatch gate reaches its
// verdict from that same decision.
func TestAuthorizeDecisionMatrix(t *testing.T) {
	for _, c := range authorizeCases() {
		t.Run(c.name, func(t *testing.T) {
			l := newAuthorizeCaseLoop(c)
			d := l.Authorize(c.tool, c.args)

			if d.Gate != c.want.gate {
				t.Errorf("gate = %v, want %v", d.Gate, c.want.gate)
			}
			if d.Origin != c.want.origin {
				t.Errorf("origin = %v, want %v", d.Origin, c.want.origin)
			}
			if d.Admitted != c.want.admitted {
				t.Errorf("admitted = %v, want %v", d.Admitted, c.want.admitted)
			}
			if d.NarrowedBy != c.want.narrowedBy {
				t.Errorf("narrowedBy = %v, want %v", d.NarrowedBy, c.want.narrowedBy)
			}
			if d.RuleMatched != c.want.ruleMatched {
				t.Errorf("ruleMatched = %v, want %v", d.RuleMatched, c.want.ruleMatched)
			}
			if d.Resource != c.want.resource {
				t.Errorf("resource = %q, want %q", d.Resource, c.want.resource)
			}
			// Provenance is only claimed when a stage actually decided.
			if c.want.origin == pipeline.OriginNone && d.Reason != "" {
				t.Errorf("origin none but reason %q: nothing decided, so nothing to explain", d.Reason)
			}
			if c.want.origin != pipeline.OriginNone && d.Reason == "" {
				t.Errorf("origin %v decided without a reason", d.Origin)
			}

			ok, err := l.admitTool(c.tool, c.args)
			if ok != c.want.dispatch {
				t.Fatalf("admitTool = (%v, %v), want allowed=%v", ok, err, c.want.dispatch)
			}
			if c.want.dispatch {
				if err != nil {
					t.Fatalf("admitted call carries an error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("refused call carries no error")
			}
			if !strings.Contains(err.Error(), c.want.refusal) {
				t.Errorf("refusal %q does not contain %q", err.Error(), c.want.refusal)
			}
		})
	}
}

// TestAuthorizeMiddlewareUsesTheSameDecision pins the other consumer: the
// approval middleware, driven through the real pipeline wiring, must reach the
// verdict the shared decision implies — for every row of the matrix and every
// global approval posture — without consulting the admission axis.
//
// The admission axis is what makes this a conformance test rather than a
// restatement: rows whose name the tool level excludes still pass the
// middleware (it classifies the model's batch before dispatch), and the
// dispatch gate is what refuses them.
func TestAuthorizeMiddlewareUsesTheSameDecision(t *testing.T) {
	for _, mode := range []string{"ask", "allow", "deny"} {
		for _, c := range authorizeCases() {
			if c.sub {
				// A sub-agent loop installs no approval middleware: nothing
				// is attached to the run to ask. Dispatch refuses its ask
				// rules instead (pinned in the matrix above).
				continue
			}
			t.Run(fmt.Sprintf("%s/mode=%s", c.name, mode), func(t *testing.T) {
				approved := false
				l := newAuthorizeCaseLoop(c)
				l.Config.ApprovalMode = mode
				l.ApproveFn = func(name, args string) bool { return approved }
				l.applyDefaults()
				pipe := l.buildPipeline()

				d := l.Authorize(c.tool, c.args)
				for _, answer := range []bool{false, true} {
					approved = answer
					msg := types.AssistantMsg("", []types.ToolCall{
						{ID: "1", Function: types.ToolCallFn{Name: c.tool, Arguments: c.args}},
					})
					step := &pipeline.Step{}
					if _, err := pipe.RunPostModel(context.Background(), &msg, step); err != nil {
						t.Fatalf("pipeline: %v", err)
					}

					wantKept := middlewareKeeps(d, mode, answer)
					if kept := len(msg.ToolCalls) == 1; kept != wantKept {
						t.Fatalf("approved=%v: call kept = %v, want %v (gate %v, origin %v)", answer, kept, wantKept, d.Gate, d.Origin)
					}
					if wantKept {
						if len(step.SynthesizedResults) != 0 {
							t.Errorf("approved=%v: kept call produced a synthesized denial", answer)
						}
						continue
					}
					if len(step.SynthesizedResults) != 1 {
						t.Fatalf("approved=%v: synthesized results = %d, want 1", answer, len(step.SynthesizedResults))
					}
					got := step.SynthesizedResults[0].Content
					if terminal := d.Gate == pipeline.GateDeny || d.Gate == pipeline.GateWithheld; terminal {
						// A terminal decision names its own cause, from the
						// decision's origin — never a policy the user does not
						// control.
						if want := "error: " + d.StripReason(c.tool); got != want {
							t.Errorf("denial = %q, want %q", got, want)
						}
					}
				}
			})
		}
	}
}

// middlewareKeeps is the approval middleware's contract with the gate axis,
// written independently of the decision path: a pass never gates, an origin
// ask always prompts, a dangerous call defers to the global mode, and deny or
// withheld is terminal.
func middlewareKeeps(d pipeline.Decision, mode string, approved bool) bool {
	switch d.Gate {
	case pipeline.GatePass:
		return true
	case pipeline.GateAsk:
		return approved
	case pipeline.GateGlobal:
		switch mode {
		case "deny":
			return false
		case "allow":
			return true
		default: // "ask" and unset
			return approved
		}
	default: // GateDeny, GateWithheld
		return false
	}
}
