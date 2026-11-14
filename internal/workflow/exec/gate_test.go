package exec

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Gate tests (bead yaah-0u0v.21). Every test drives the real reducer through
// the real driver against FakeRuntime: scripted outcomes, virtual clock, no
// sleeping. What is pinned here: the assert path's pass/fail routing and its
// escalation when no on_fail arm exists, the judge path's structured verdict
// (and its `ok`/`verdict` spellings), the disposition reaching a downstream
// step as a value, the declaration refusals, and the determinism contract.

const gatePassYAML = `  steps:
    - id: a
      kind: emit
      value: hello
    - id: g
      kind: gate
      needs: [a]
      assert:
        name: nonempty
      on_pass: p
      on_fail: f
    - id: p
      kind: emit
      needs: [g]
      value: pass-branch
    - id: f
      kind: emit
      needs: [g]
      value: fail-branch
`

func gateGraph(t *testing.T, body string) *ir.Graph {
	t.Helper()
	y := strings.Replace(specHead, "%s", "gate", 1) + body
	return graphFromYAML(t, y)
}

// TestGateAssertPassRoutesOnPass drives the assert path: the subject resolves
// through the gate's needs, the check passes, the on_pass arm runs and the
// on_fail arm is skipped, and the gate itself records a `pass` disposition.
func TestGateAssertPassRoutesOnPass(t *testing.T) {
	g := gateGraph(t, gatePassYAML)
	res, fake := runGraph(t, g, nil, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s, want succeeded (reason %q)", res.Status, res.Reason)
	}
	n := nodeOf(t, res, "steps.g")
	if n.State != StateSucceeded {
		t.Fatalf("gate state = %s (%q)", n.State, n.Reason)
	}
	if n.Disposition != DispositionPass {
		t.Errorf("disposition = %q, want %q", n.Disposition, DispositionPass)
	}
	if got := nodeOf(t, res, "steps.p"); got.State != StateSucceeded {
		t.Errorf("on_pass arm state = %s, want succeeded (reason %q)", got.State, got.Reason)
	}
	if got := nodeOf(t, res, "steps.f"); got.State != StateSkipped || !strings.Contains(got.Reason, ReasonGateArmNotTaken) {
		t.Errorf("on_fail arm = %s %q, want skipped with %s", got.State, got.Reason, ReasonGateArmNotTaken)
	}
	if len(fake.Started()) != 0 {
		t.Errorf("assert gate dispatched %d attempt(s), want 0", len(fake.Started()))
	}
}

// TestGateAssertFailRoutesOnFail drives the failure half with an on_fail arm:
// the gate records `fail`, routes to on_fail, skips on_pass, and still ends
// SUCCEEDED — a disposition is a value, not the node's own failure.
func TestGateAssertFailRoutesOnFail(t *testing.T) {
	y := strings.Replace(gatePassYAML, "name: nonempty", "name: count_ge\n        args:\n          n: 5", 1)
	g := gateGraph(t, y)
	res, _ := runGraph(t, g, nil, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s, want succeeded (reason %q)", res.Status, res.Reason)
	}
	n := nodeOf(t, res, "steps.g")
	if n.State != StateSucceeded {
		t.Fatalf("gate state = %s, want succeeded (a fail with on_fail is a value, not a failure; reason %q)", n.State, n.Reason)
	}
	if n.Disposition != DispositionFail {
		t.Errorf("disposition = %q, want %q", n.Disposition, DispositionFail)
	}
	if got := nodeOf(t, res, "steps.f"); got.State != StateSucceeded {
		t.Errorf("on_fail arm state = %s, want succeeded (reason %q)", got.State, got.Reason)
	}
	if got := nodeOf(t, res, "steps.p"); got.State != StateSkipped || !strings.Contains(got.Reason, ReasonGateArmNotTaken) {
		t.Errorf("on_pass arm = %s %q, want skipped with %s", got.State, got.Reason, ReasonGateArmNotTaken)
	}
}

// TestGateAssertFailEscalatesWithoutOnFail pins the escalation path: a gate
// with no on_fail arm ends FAILED, which the run's own on_failure disposition
// (abort by default) then raises.
func TestGateAssertFailEscalatesWithoutOnFail(t *testing.T) {
	y := `  steps:
    - id: g
      kind: gate
      assert:
        name: nonempty
`
	g := gateGraph(t, y)
	res, _ := runGraph(t, g, nil, Config{})
	n := nodeOf(t, res, "steps.g")
	if n.State != StateFailed || !strings.Contains(n.Reason, ReasonGateFailed) {
		t.Fatalf("gate = %s %q, want failed with %s", n.State, n.Reason, ReasonGateFailed)
	}
	if n.Disposition != DispositionFail {
		t.Errorf("disposition = %q, want %q", n.Disposition, DispositionFail)
	}
}

// TestGateJudgeRoutesByVerdict drives the judge half: the gate dispatches its
// judge (whose role comes from the judge payload, not the node), parses the
// structured verdict, and routes on it.
func TestGateJudgeRoutesByVerdict(t *testing.T) {
	for _, c := range []struct {
		name    string
		verdict string
		pass    bool
	}{
		{"pass", `{"pass": true, "reason": "looks good"}`, true},
		{"ok-spelling", `{"ok": true}`, true},
		{"verdict-word", `{"verdict": "fail"}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			y := `  steps:
    - id: g
      kind: gate
      judge: {role: developer, prompt: "is this ok?"}
      on_pass: p
      on_fail: f
    - id: p
      kind: emit
      needs: [g]
      value: pass-branch
    - id: f
      kind: emit
      needs: [g]
      value: fail-branch
`
			g := gateGraph(t, y)
			script := map[string][]Completion{"steps.g": {Report(c.verdict, 0)}}
			res, fake := runGraph(t, g, script, Config{})
			if res.Status != RunSucceeded {
				t.Fatalf("status = %s, want succeeded (reason %q)", res.Status, res.Reason)
			}
			n := nodeOf(t, res, "steps.g")
			if n.State != StateSucceeded {
				t.Fatalf("gate state = %s (%q)", n.State, n.Reason)
			}
			if n.GateVerdict != c.verdict {
				t.Errorf("GateVerdict = %q, want %q", n.GateVerdict, c.verdict)
			}
			wantDisposition := DispositionFail
			if c.pass {
				wantDisposition = DispositionPass
			}
			if n.Disposition != wantDisposition {
				t.Errorf("disposition = %q, want %q", n.Disposition, wantDisposition)
			}
			if len(fake.Started()) != 1 {
				t.Errorf("judge dispatch count = %d, want 1", len(fake.Started()))
			}
			taken, skipped := "steps.f", "steps.p"
			if c.pass {
				taken, skipped = "steps.p", "steps.f"
			}
			if got := nodeOf(t, res, taken); got.State != StateSucceeded {
				t.Errorf("taken arm %s = %s, want succeeded (reason %q)", taken, got.State, got.Reason)
			}
			if got := nodeOf(t, res, skipped); got.State != StateSkipped {
				t.Errorf("skipped arm %s = %s, want skipped", skipped, got.State)
			}
		})
	}
}

// TestGateJudgeEscalation pins the judge's escalate marker: the verdict fails
// AND asks to escalate, which ends the gate failed regardless of an on_fail arm.
func TestGateJudgeEscalation(t *testing.T) {
	y := `  steps:
    - id: g
      kind: gate
      judge: {role: developer, prompt: "is this ok?"}
      on_pass: p
      on_fail: f
    - id: p
      kind: emit
      needs: [g]
      value: pass-branch
    - id: f
      kind: emit
      needs: [g]
      value: fail-branch
`
	g := gateGraph(t, y)
	script := map[string][]Completion{"steps.g": {Report(`{"pass": false, "escalate": true}`, 0)}}
	res, _ := runGraph(t, g, script, Config{})
	n := nodeOf(t, res, "steps.g")
	if n.State != StateFailed || !strings.Contains(n.Reason, ReasonGateEscalated) {
		t.Fatalf("gate = %s %q, want failed with %s", n.State, n.Reason, ReasonGateEscalated)
	}
	if n.Disposition != DispositionEscalate {
		t.Errorf("disposition = %q, want %q", n.Disposition, DispositionEscalate)
	}
	// The skipped arms must quote the gate's own failure, not branch
	// vocabulary: a failed gate never selected an arm, so "routed to no arm"
	// would lie about what happened.
	for _, arm := range []string{"steps.p", "steps.f"} {
		got := nodeOf(t, res, arm)
		if got.State != StateSkipped {
			t.Errorf("arm %s = %s, want skipped", arm, got.State)
			continue
		}
		if !strings.Contains(got.Reason, ReasonGateEscalated) {
			t.Errorf("arm %s reason = %q, want the gate's own %s", arm, got.Reason, ReasonGateEscalated)
		}
	}
}

// TestGateDispositionReachesDownstream pins the value plumbing: a later step
// reads the disposition and the boolean through the ordinary selector scope.
func TestGateDispositionReachesDownstream(t *testing.T) {
	y := gatePassYAML + `    - id: show
      kind: emit
      needs: [g]
      value: '{{ steps.g.output.disposition }}'
`
	g := gateGraph(t, y)
	res, _ := runGraph(t, g, nil, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s, want succeeded (reason %q)", res.Status, res.Reason)
	}
	show := nodeOf(t, res, "steps.show")
	if show.State != StateSucceeded {
		t.Fatalf("show state = %s (%q)", show.State, show.Reason)
	}
	if len(show.Outputs) != 1 || show.Outputs[0].Value != DispositionPass {
		t.Fatalf("rendered output = %+v, want %q", show.Outputs, DispositionPass)
	}
}

// TestGateRefusals pins the declaration fence: every malformed gate ends
// refused with its named reason and dispatches nothing.
func TestGateRefusals(t *testing.T) {
	cases := []struct {
		name    string
		node    ir.Node
		wantSub string
	}{
		{
			"neither", ir.Node{Path: "steps.g", Kind: ir.KindGate},
			ReasonGateDeclarationInvalid,
		},
		{
			"both",
			ir.Node{Path: "steps.g", Kind: ir.KindGate,
				Assert: &ir.Assert{Name: "nonempty"},
				Judge:  &ir.Judge{Role: "developer", Prompt: &ir.Template{}}},
			ReasonGateDeclarationInvalid,
		},
		{
			"unknown-assert", ir.Node{Path: "steps.g", Kind: ir.KindGate,
				Assert: &ir.Assert{Name: "nope"}},
			ReasonGateAssertUnknown,
		},
		{
			"bad-args", ir.Node{Path: "steps.g", Kind: ir.KindGate,
				Assert: &ir.Assert{Name: "count_ge", Args: map[string]any{"n": "three"}}},
			ReasonGateAssertArgs,
		},
		{
			"judge-missing-role", ir.Node{Path: "steps.g", Kind: ir.KindGate,
				Judge: &ir.Judge{Prompt: &ir.Template{}}},
			ReasonGateJudgeMissing,
		},
		{
			"judge-empty-prompt", ir.Node{Path: "steps.g", Kind: ir.KindGate,
				Judge: &ir.Judge{Role: "developer", Prompt: &ir.Template{Raw: "   "}}},
			ReasonGateJudgeMissing,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			action, note := classifyKind(&c.node)
			if action != kindRefuse {
				t.Fatalf("action = %v, want refuse", action)
			}
			if !strings.Contains(note, c.wantSub) {
				t.Errorf("refusal = %q, want substring %q", note, c.wantSub)
			}
		})
	}
}

// TestGateDeterminism pins the determinism contract for the assert path: the
// same graph produces a byte-identical event sequence across runs.
func TestGateDeterminism(t *testing.T) {
	g := gateGraph(t, gatePassYAML)
	res1, _ := runGraph(t, g, nil, Config{})
	res2, _ := runGraph(t, g, nil, Config{})
	if eventLog(res1) != eventLog(res2) {
		t.Errorf("event sequences differ:\nrun1:\n%s\nrun2:\n%s", eventLog(res1), eventLog(res2))
	}
}
