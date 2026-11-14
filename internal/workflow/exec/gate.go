package exec

import (
	"fmt"
	"strconv"
	"strings"

	wfassert "github.com/buchenberg/yaah/internal/workflow/assert"
	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Gate execution (yaah-0u0v.21).
//
// A gate is a binary branch with a COMPUTED condition: it asks one question,
// records the answer as a value, and routes on it. The question is decided in
// one of exactly two ways, and never in a third:
//
//   - `assert:` names a check in the fixed registry (internal/workflow/assert).
//     The check is a Go function, so the whole evaluation happens inside a
//     reducer cycle, dispatches nothing, and costs no budget. "Named asserts
//     only" is the design: a free-form expression would be a second language to
//     specify, sandbox, and validate, and this core promises determinism only
//     for code it ships.
//   - `judge:` names a role and a prompt, and the judge's dispatch IS the gate's
//     attempt — the same shape a select's `policy: judge` has (yaah-0u0v.20).
//     The delegate delivers a structured Verdict, which is parsed rather than
//     trusted, and a verdict nobody can read is refused instead of guessed at.
//
// # Gate results are values, not control flow hidden in a prompt
//
// The gate's outcome is recorded on the node as a value — Disposition ("pass",
// "fail", or "escalate"), the reason the check or the judge stated, the assert
// name or the verdict body that decided — and `steps.<id>.output` reads that
// value through the ordinary scope, so a downstream emit, settle, or judge can
// quote the decision without re-running it. What the node's STATE says depends
// on what the author declared:
//
//   - pass: the gate SUCCEEDED and routes to on_pass (also succeeded when no
//     arm is declared: routing is the gate's whole job);
//   - fail with on_fail declared: the gate SUCCEEDED and routes to on_fail. A
//     failure the document answered with an arm is a route, not a run-level
//     failure — exactly the reading a branch's unmatched case takes;
//   - fail with no on_fail, or a judge verdict that asks to ESCALATE: the gate
//     ends FAILED with a reason naming the check and what it said. That is the
//     escalation path, and it is the ordinary failure path on purpose: the
//     node's own `on_failure` (abort by default, continue, skip) then decides
//     whether the run stops or drains with the failure exposed. A gate needs no
//     second escalation mechanism when the run already has one.
//
// # Routing
//
// The taken successor becomes reachable and the arm the gate did not take is
// skipped with a gate-specific reason, using the same routing relation a branch
// keeps (branch.go's arms/armRouting/skipUnselectedArms). A gate that never
// routes at all — a refusal, or a terminal failure — skips BOTH arms, because
// neither of them is on this path through the graph and a pending arm nobody
// will ever release would otherwise end cancelled with no explanation.
//
// # Refusal over crash
//
// Every way a gate can be unusable is a named terminal reason, never a panic:
// neither assert nor judge declared, both declared, an assert name outside the
// registry, arguments the check does not read, a judge with no role, a subject
// the run cannot read, and a verdict that is not a verdict.

// Reasons the gate family states.
const (
	// ReasonGateDeclarationInvalid prefixes a gate that names no condition, or
	// two of them: a gate has exactly one question, and a second would leave
	// the run choosing which answer wins.
	ReasonGateDeclarationInvalid = "gate_declaration_invalid"
	// ReasonGateAssertUnknown names an assert outside the registry allowlist.
	ReasonGateAssertUnknown = "gate_assert_unknown"
	// ReasonGateAssertArgs names arguments the named check does not read, or
	// reads in another shape.
	ReasonGateAssertArgs = "gate_assert_args_invalid"
	// ReasonGateJudgeMissing names a judge with no role (or no prompt): nobody
	// is named to decide.
	ReasonGateJudgeMissing = "gate_judge_missing"
	// ReasonGateSubjectUnavailable prefixes an assert whose subject cannot be
	// read from this run: a `from` naming no step, or one that is not in reach.
	ReasonGateSubjectUnavailable = "gate_subject_unavailable"
	// ReasonGateVerdictInvalid prefixes a judge result that is not a structured
	// verdict at all, or one that contradicts itself.
	ReasonGateVerdictInvalid = "gate_verdict_invalid"
	// ReasonGateFailed prefixes a failing assert with no on_fail to route to.
	// The author's on_failure disposition raises it from here.
	ReasonGateFailed = "gate_assertion_failed"
	// ReasonGateEscalated prefixes a judge verdict that asks for a human
	// decision. It is terminal whatever on_fail says: an escalation is the
	// judge's own refusal to decide, so routing past it would answer the
	// question the judge declined to.
	ReasonGateEscalated = "gate_escalated"
	// ReasonGateArmUnknown names an on_pass/on_fail that resolves to no step in
	// this graph. ir.resolveGate refuses it in an authored document, so only a
	// hand-built graph reaches this.
	ReasonGateArmUnknown = "gate_arm_unknown"
	// ReasonGateArmNotTaken names a gate arm the gate did not route to. It is
	// the reason a skip is declared, not a failure: the author declared the
	// routing.
	ReasonGateArmNotTaken = "gate_arm_not_taken"
)

// Gate disposition vocabulary. These strings are the value a gate reports, so
// they are the words a selector matches on and a report quotes.
const (
	// DispositionPass is an assert that held, or a judge that said pass.
	DispositionPass = "pass"
	// DispositionFail is an assert that did not hold, or a judge that said fail.
	DispositionFail = "fail"
	// DispositionEscalate is a judge that declined to decide.
	DispositionEscalate = "escalate"
)

// gateAssert reports whether the gate's condition is the deterministic half.
// classifyKind uses it to decide between a reducer step and a dispatch, so it is
// the one place that reading lives.
func gateAssert(n *ir.Node) bool {
	return n != nil && n.Kind == ir.KindGate && n.Assert != nil && n.Judge == nil
}

// gateStepOnce drives one deterministic assert per cycle: the first ready gate
// whose subject can be resolved is evaluated and reported. A subject whose
// producer has not terminated is a WAIT, not an empty value — the same rule
// fan-out, aggregate, settlement planning, and selection follow — so the cycle
// falls through and the run does whatever else it can.
//
// The check itself is evaluated HERE rather than inside Apply for the same
// reason a branch's case matching is: the driver reports what it read, and the
// reducer decides what that means for the graph. Both halves are pure, so the
// split costs nothing in determinism and keeps the reducer the one place state
// changes.
func (r *Run) gateStepOnce() (bool, error) {
	for _, p := range r.ready {
		n := r.byPath[p]
		if !gateAssert(n) {
			continue
		}
		st := r.state[p]
		if st == nil || st.State != StateReady || st.Disposition != "" {
			continue
		}
		subject, deferred, refusal := r.gateSubject(n)
		if deferred {
			continue
		}
		if refusal != "" {
			return true, r.Apply(GateEvaluatedEvent{NodePath: p, Assert: n.Assert.Name, Refusal: refusal})
		}
		spec, ok := wfassert.Lookup(n.Assert.Name)
		if !ok {
			// classifyKind refused this before the node could run; reaching here
			// would mean the table and the registry disagreed, so the honest
			// answer is the refusal rather than a nil-check panic.
			return true, r.Apply(GateEvaluatedEvent{
				NodePath: p,
				Assert:   n.Assert.Name,
				Refusal: ReasonGateAssertUnknown + ": " + n.Path + " " +
					wfassert.Problem(n.Assert.Name, n.Assert.Args),
			})
		}
		pass, reason := spec.Check(wfassert.Input{Value: subject, Args: checkArgs(n.Assert)})
		return true, r.Apply(GateEvaluatedEvent{
			NodePath: p,
			Assert:   n.Assert.Name,
			Value:    flatten(scalarText(subjectValueText(subject))),
			Pass:     pass,
			Reason:   reason,
		})
	}
	return false, nil
}

// checkArgs is the assert's declared arguments without the reserved subject
// key: `from` selects the check's input, so handing it to the check would be
// handing it a step id it never asked about.
func checkArgs(a *ir.Assert) map[string]any {
	if a == nil || len(a.Args) == 0 {
		return nil
	}
	out := make(map[string]any, len(a.Args))
	for k, v := range a.Args {
		if k == wfassert.SubjectKey {
			continue
		}
		out[k] = v
	}
	return out
}

// subjectValueText renders the subject for the event log without quoting it
// wholesale: a list of mappings is one field, not a page.
func subjectValueText(v any) any {
	switch t := v.(type) {
	case nil:
		return ""
	case []any:
		return strconv.Itoa(len(t)) + " entries"
	case []string:
		return strconv.Itoa(len(t)) + " entries"
	case map[string]any:
		return strconv.Itoa(len(t)) + " keys"
	case string:
		return t
	}
	return v
}

// gateSubject resolves the value a gate's assert reads, and states why it has
// none when it has none. The three answers mirror fanoutItems and
// selectCandidates: a producer that has not terminated DEFERS (a wait), an
// unreadable reference is a REFUSAL the reducer turns into the node's terminal
// state, and anything else is the value.
//
// The subject is the recorded value of the step named by the reserved `from`
// argument. When the author names none, it is the recorded values of the gate's
// own needs in declaration order — the values the gate depends on, which is
// what an assert over "the work I wait for" means. A need or a `from` step that
// did not succeed contributes nil rather than an empty string, so a check can
// tell "nothing was produced" from "an empty value was produced".
func (r *Run) gateSubject(n *ir.Node) (value any, deferred bool, refusal string) {
	from := ""
	if n.Assert != nil {
		if s, ok := n.Assert.Args[wfassert.SubjectKey].(string); ok {
			from = strings.TrimSpace(s)
		}
	}
	if from != "" {
		path, ok := r.gateSubjectPath(n, from)
		if !ok {
			return nil, false, ReasonGateSubjectUnavailable + ": " + n.Path + " reads " +
				strconv.Quote(from) + ", which is not a step in this graph"
		}
		st := r.state[path]
		if st == nil {
			return nil, false, ReasonGateSubjectUnavailable + ": " + n.Path + " reads " +
				strconv.Quote(from) + ", which is not a step in this run"
		}
		if !st.State.Terminal() {
			return nil, true, ""
		}
		if st.State != StateSucceeded {
			// A step that ended failed, skipped, or cancelled produced no value;
			// the check is handed nil and decides for itself whether that is a
			// failure (every check in the registry says it is).
			return nil, false, ""
		}
		return r.stepValue(r.byPath[path], st), false, ""
	}
	needs, ok := r.needPaths(n)
	if !ok {
		return nil, false, ReasonGateSubjectUnavailable + ": " + n.Path +
			" declares a need that names no step, so its subject cannot be resolved"
	}
	values := make([]any, 0, len(needs))
	for _, p := range needs {
		st := r.state[p]
		if st == nil || !st.State.Terminal() {
			return nil, true, ""
		}
		if st.State != StateSucceeded {
			values = append(values, nil)
			continue
		}
		values = append(values, r.stepValue(r.byPath[p], st))
	}
	return values, false, ""
}

// gateSubjectPath resolves a `from` reference: an instance-local step id first,
// then a top-level step id, then a node path. The order is the shadowing rule
// every reference in this package follows — a body-local id names the step in
// its own item — and accepting the path as well keeps a hand-built graph
// usable.
func (r *Run) gateSubjectPath(n *ir.Node, from string) (string, bool) {
	if inst := r.instances[n.Path]; inst != nil {
		if p, ok := inst.locals[from]; ok {
			return p, true
		}
	}
	if dep, ok := r.byID[from]; ok {
		return dep.Path, true
	}
	if _, ok := r.byPath[from]; ok {
		return from, true
	}
	return "", false
}

// onGateEvaluated applies one gate decision: it records the disposition as the
// node's value, then routes, fails, or escalates. It is the single place a gate
// becomes an outcome, so the deterministic assert and the judge's verdict
// cannot disagree about what closing a gate means.
func (r *Run) onGateEvaluated(e GateEvaluatedEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: gate_evaluated for unknown node %q", e.NodePath)
	}
	if st.State.Terminal() {
		return fmt.Errorf("workflow/exec: gate_evaluated for node %s, which is already %s", e.NodePath, st.State)
	}
	n := r.byPath[e.NodePath]
	if n == nil || n.Kind != ir.KindGate {
		return fmt.Errorf("workflow/exec: gate_evaluated for node %q, which is not a gate", e.NodePath)
	}
	if e.Refusal != "" {
		return r.failGate(n, e.Refusal)
	}
	disposition := DispositionFail
	switch {
	case e.Pass:
		disposition = DispositionPass
	case e.Escalate:
		disposition = DispositionEscalate
	}
	st.Disposition = disposition
	st.GateAssert = e.Assert
	st.GateVerdict = e.Verdict
	st.GateReason = e.Reason
	st.Result = disposition
	r.recordOutput(st, NodeOutput{
		NodePath: n.Path,
		Kind:     n.Kind,
		Name:     gateBindingName(n),
		Value:    disposition,
	})
	switch {
	case disposition == DispositionEscalate:
		return r.failGate(n, ReasonGateEscalated+": "+gateReasonText(e.Reason))
	case disposition == DispositionFail && n.OnFail == "":
		// The escalation path: a failing gate with no arm to route to is the
		// node's own failure, and the author's on_failure disposition (abort by
		// default) raises it from there.
		return r.failGate(n, ReasonGateFailed+": "+gateDecisionSource(n, e)+" "+gateReasonText(e.Reason))
	}
	armID := n.OnPass
	if disposition == DispositionFail {
		armID = n.OnFail
	}
	selected := ""
	if armID != "" {
		dep, ok := r.byID[armID]
		if !ok {
			return r.failGate(n, ReasonGateArmUnknown+": "+n.Path+" routes to "+armID+
				", which is not a step in this graph")
		}
		selected = dep.Path
	}
	// The arm is recorded BEFORE the gate terminates, for the reason a branch's
	// is: the gate's own termination releases arms that need it, and an arm
	// released into a still-undecided gate would have to be re-examined.
	st.Selected = selected
	if err := r.derive(NodeSucceededEvent{NodePath: n.Path, Outputs: outputNames(st)}); err != nil {
		return err
	}
	return r.skipUnselectedArms(n, selected)
}

// failGate ends a gate failed with a reason, then settles both arms: a gate
// that never routed has no arm on this path through the graph, and a pending
// arm nobody will release would end cancelled for no stated reason.
func (r *Run) failGate(n *ir.Node, reason string) error {
	if err := r.failTerminal(n.Path, reason); err != nil {
		return err
	}
	return r.skipUnselectedArms(n, "")
}

// gateDecisionSource names what decided, so the reason reads "assert nonempty"
// or "judge verdict" rather than an anonymous failure.
func gateDecisionSource(n *ir.Node, e GateEvaluatedEvent) string {
	if e.Verdict != "" {
		return "judge verdict"
	}
	if e.Assert != "" {
		return "assert " + e.Assert
	}
	if n.Assert != nil {
		return "assert " + n.Assert.Name
	}
	return "judge verdict"
}

// gateReasonText keeps an empty reason readable: no check in the registry
// returns one, but a judge's verdict body may carry none.
func gateReasonText(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "the check stated no reason"
	}
	return reason
}

// gateBindingName is the name a gate's value is bound under: its declared
// output name, else its step id, else its path — the rule emit, settle,
// fan-out, aggregate, and select follow, so `steps.<id>.output` and a named
// binding read one value.
func gateBindingName(n *ir.Node) string {
	if n.Output != nil && n.Output.Name != "" {
		return n.Output.Name
	}
	if n.ID != "" {
		return n.ID
	}
	return n.Path
}

// gateValue is a gate's recorded value: the disposition as a word and as the
// boolean a predicate can match, the reason that produced it, and — under a
// judge — the verdict body verbatim. It is structured for the same reason a
// select's decision is: `steps.<id>.output.disposition` is what a downstream
// node routes on, and it must not require re-parsing a log line.
func (st *NodeState) gateValue() map[string]any {
	out := map[string]any{
		"disposition": st.Disposition,
		"pass":        st.Disposition == DispositionPass,
		"reason":      st.GateReason,
	}
	if st.GateAssert != "" {
		out["assert"] = st.GateAssert
	}
	if st.GateVerdict != "" {
		out["verdict"] = st.GateVerdict
	}
	return out
}

// Verdict schema (yaah-0u0v.21). A judge delegate delivers a JSON object, and
// the gate reads exactly three fields from it:
//
//	{"pass": true|false, "escalate": true|false, "reason": "why"}
//
// `pass` may also be spelled `ok` (a boolean) or `verdict` (the string "pass" or
// "fail"), because a model asked for a verdict writes one of those three more
// often than it writes a schema name. `escalate` is optional and means the judge
// declines to decide: the gate ends failed and the run's on_failure disposition
// raises it, which is the escalation path. A verdict that passes AND escalates
// contradicts itself and is refused, and so is one that names none of the three
// pass spellings: a judge whose answer nobody can read has not decided anything.
type gateVerdict struct {
	pass     bool
	escalate bool
	reason   string
}

// parseGateVerdict reads a judge's delivered result.
func parseGateVerdict(text string) (gateVerdict, string) {
	obj, ok := decodeJSONObject(text)
	if !ok {
		return gateVerdict{}, ReasonGateVerdictInvalid +
			": the judge's result is not a JSON object, so it states no verdict"
	}
	out := gateVerdict{}
	if why, ok := obj["reason"].(string); ok {
		out.reason = why
	}
	if v, ok := obj["escalate"].(bool); ok {
		out.escalate = v
	}
	decided := false
	for _, key := range []string{"pass", "ok"} {
		if v, ok := obj[key].(bool); ok {
			out.pass = v
			decided = true
			break
		}
	}
	if !decided {
		if v, ok := obj["verdict"].(string); ok {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case DispositionPass:
				out.pass, decided = true, true
			case DispositionFail:
				out.pass, decided = false, true
			case DispositionEscalate:
				out.escalate, decided = true, true
			}
		}
	}
	if !decided {
		return gateVerdict{}, ReasonGateVerdictInvalid +
			": the judge's verdict names no pass field (pass, ok, or verdict: pass|fail)"
	}
	if out.pass && out.escalate {
		return gateVerdict{}, ReasonGateVerdictInvalid +
			": the judge's verdict asks to pass and to escalate at once, and a gate has one answer"
	}
	return out, ""
}

// foldGateVerdict turns a completed judge attempt into the gate's decision. The
// judge's delivered body is recorded on the node FIRST, so a trace can quote what
// decided as well as what was decided, and the decision itself is the same
// event the deterministic half submits — one handler, two sources.
func (r *Run) foldGateVerdict(n *ir.Node, st *NodeState) error {
	st.GateVerdict = st.Result
	v, why := parseGateVerdict(st.Result)
	if why != "" {
		return r.failGate(n, why)
	}
	return r.derive(GateEvaluatedEvent{
		NodePath: n.Path,
		Verdict:  st.Result,
		Pass:     v.pass,
		Escalate: v.escalate,
		Reason:   v.reason,
	})
}
