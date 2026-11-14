package exec

import (
	"fmt"
	"sort"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Branch execution (yaah-0u0v.15).
//
// A branch ROUTES. It dispatches nothing, settles nothing, and renders nothing: it
// evaluates its `on` selector against recorded run state, matches the resolved
// value against its cases by LITERAL equality — v1 has no expression evaluation
// and this bead adds none — and decides which arm runs. Everything after that
// decision is the ordinary scheduler: the selected arm is an ordinary node
// (released through join-all if it needs the branch, already ready if it does
// not), and the arms the branch did not select terminate SKIPPED with a reason
// naming the branch and the arm it chose.
//
// # Why the arms are gated
//
// validate's linearity model treats a branch's arms as ALTERNATIVES — "anything
// downstream of a guard is an alternative" (markGuarded) — so exactly one arm runs
// on any one path through the graph. The executor holds the same reading, which is
// why an unselected arm ends skipped even when it does NOT declare the branch as a
// need. Two mechanisms keep that honest:
//
//   - the driver will not start a node that is an arm of a branch that has not
//     terminated yet (armRouting), so routing cannot be overtaken by the ready
//     queue's path order — an arm named "steps.alpha" sorts before its branch and
//     would otherwise start first;
//   - the reducer applies the decision the moment it is made, skipping every
//     non-terminal unselected arm whether it is pending or already ready, and
//     settling the question again at arm-ready time for arms released later.
//
// # What a branch cannot do yet
//
// A predicate on `on` is REFUSED rather than read. Cases are the branch's matching
// mechanism, and a predicate would be a second one; the run would then have to
// decide which of the two answers wins, and either choice would be an invented
// semantic. `when:` guards on the arms express per-arm conditions, which is the
// mechanism the vocabulary already has.
//
// A branch with no matching case and no `default` routes nothing: it closes
// succeeded — routing was its whole job and it found no arm — and the run states
// the gap in Result.Warnings rather than failing a graph whose routing declaration
// validate already reports (armsTotal requires a default).

// Reasons the branch family states.
const (
	// ReasonBranchSourceUnavailable prefixes a branch the run cannot route: no
	// `on` source, a producer that did not succeed, an `on` carrying a predicate,
	// or a case naming a step that is not in the graph.
	ReasonBranchSourceUnavailable = "branch_source_unavailable"
	// ReasonBranchArmNotSelected names an arm the branch did not choose. It is the
	// reason a skip is declared, not a failure: the author declared the routing.
	ReasonBranchArmNotSelected = "branch_arm_not_selected"
)

// armIDs lists the step ids a node can route to, in declaration order. A branch
// names its arms through every case's `then` and then the `default`; a gate
// names exactly two, on_pass first and then on_fail, because a pass is the arm
// an author reads first (yaah-0u0v.21).
func armIDs(n *ir.Node) []string {
	if n == nil {
		return nil
	}
	if n.Kind == ir.KindGate {
		out := make([]string, 0, 2)
		if n.OnPass != "" {
			out = append(out, n.OnPass)
		}
		if n.OnFail != "" {
			out = append(out, n.OnFail)
		}
		return out
	}
	out := make([]string, 0, len(n.Cases)+1)
	for _, c := range n.Cases {
		out = append(out, c.Then)
	}
	if n.Default != "" {
		out = append(out, n.Default)
	}
	return out
}

// registerArms records the routing relation of one branch: which node paths it
// decides about. It is a ROUTING relation and not a dependency — an arm may
// declare the branch as a need, and then it is both — so it is kept beside the
// dependency graph rather than inside it.
//
// An unknown or self-naming arm is skipped here: ir.resolveBranch refuses both in
// an authored graph, and a hand-built one is refused again when the branch routes.
func (r *Run) registerArms(n *ir.Node) {
	if n == nil || (n.Kind != ir.KindBranch && n.Kind != ir.KindGate) {
		return
	}
	for _, id := range armIDs(n) {
		dep, ok := r.byID[id]
		if !ok || dep.Path == n.Path {
			continue
		}
		if r.arms[dep.Path] == nil {
			r.arms[dep.Path] = map[string]bool{}
		}
		r.arms[dep.Path][n.Path] = true
	}
}

// armRouting answers a node's routing position: whether a branch naming it has
// selected it, and whether that decision is available at all. A node no branch
// names is always selected, and undecided is true while any branch that names it
// has not terminated — which is what the driver holds an arm for.
//
// An arm named by two branches runs if EITHER selected it, which is the reading
// that cannot silently drop work: two branches naming one step are two ways to
// reach it, exactly as two `when:` guards would be.
func (r *Run) armRouting(path string) (selected, undecided bool) {
	owners := r.arms[path]
	if len(owners) == 0 {
		return true, false
	}
	for b := range owners {
		st := r.state[b]
		if st == nil || !st.State.Terminal() {
			return false, true
		}
		if st.State == StateSucceeded && st.Selected == path {
			return true, false
		}
	}
	return false, false
}

// armSkipReason renders why one arm was not run, naming the branch that decided
// and the arm it chose instead. Owner branches are visited in canonical order, so a
// node two branches could reach names the same one every run.
func (r *Run) armSkipReason(path string) string {
	owners := make([]string, 0, len(r.arms[path]))
	for b := range r.arms[path] {
		owners = append(owners, b)
	}
	sort.Strings(owners)
	for _, b := range owners {
		st := r.state[b]
		if st != nil && st.State.Terminal() && st.Selected != path {
			return notTakenReason(r.byPath[b], b, st)
		}
	}
	return ReasonBranchArmNotSelected + ": no branch selected " + path
}

// notTakenReason states why one routing owner did not take one arm. A branch and
// a gate share the mechanism and not the vocabulary: a gate's decision is a
// condition that held or did not (yaah-0u0v.21), so its arms say so. A gate that
// terminated without routing at all — a refusal, or a failure with no on_fail —
// names that instead of quoting an arm it never chose.
func notTakenReason(n *ir.Node, owner string, st *NodeState) string {
	if n != nil && n.Kind == ir.KindGate {
		if st.State != StateSucceeded {
			return ReasonGateArmNotTaken + ": " + owner + " ended " + string(st.State) +
				" without selecting an arm (" + st.Reason + ")"
		}
		return ReasonGateArmNotTaken + ": " + owner + " routed to " + routedLabel(st.Selected)
	}
	return ReasonBranchArmNotSelected + ": " + owner + " routed to " + routedLabel(st.Selected)
}

// routedLabel renders a branch's decision as one log/reason field.
func routedLabel(selected string) string {
	if selected == "" {
		return "no arm (no case matched and no default is declared)"
	}
	return selected
}

// branchStepOnce drives one routing cycle: the first ready branch whose `on`
// source can be read is decided. A branch whose producer has not terminated is a
// WAIT, not an empty value, so the cycle falls through and the run does whatever
// else it can.
func (r *Run) branchStepOnce() (bool, error) {
	for _, p := range r.ready {
		n := r.byPath[p]
		if n == nil || n.Kind != ir.KindBranch {
			continue
		}
		st := r.state[p]
		if st == nil || st.State != StateReady {
			continue
		}
		advanced, err := r.branchStep(n)
		if err != nil {
			return true, err
		}
		if advanced {
			return true, nil
		}
	}
	return false, nil
}

// branchStep resolves one branch's routed value and submits the decision. The
// selector evaluation is the driver's half — the same split a settlement plan
// follows — and everything it implies for node state happens inside Apply.
func (r *Run) branchStep(n *ir.Node) (bool, error) {
	sel := n.On
	if sel == nil || sel.From == nil {
		return true, r.Apply(BranchRoutedEvent{
			NodePath: n.Path,
			Refusal:  ReasonBranchSourceUnavailable + ": " + n.Path + " declares no on selector to route on",
		})
	}
	if sel.Predicate.Op != ir.PredNone || sel.Predicate.Field != "" {
		return true, r.Apply(BranchRoutedEvent{
			NodePath: n.Path,
			Refusal: ReasonBranchSourceUnavailable + ": " + n.Path +
				".on carries a predicate; a branch routes on the value its cases match, and a second matching mechanism would leave the run deciding which of the two answers wins",
		})
	}
	if p := r.awaitingProducer(sel); p != "" {
		return false, nil
	}
	v, err := r.selectorValue(sel)
	if err != nil {
		return true, r.Apply(BranchRoutedEvent{
			NodePath: n.Path,
			Refusal:  ReasonBranchSourceUnavailable + ": " + err.Error(),
		})
	}
	if sel.AsList {
		v = []any{v}
	}
	return true, r.Apply(BranchRoutedEvent{
		NodePath: n.Path,
		Value:    flatten(scalarText(v)),
		Selected: matchCase(n, v),
	})
}

// matchCase returns the arm a branch routes to: the first case whose literal
// equals the resolved value, else the default, else "" for "no case matched and no
// default was declared". Equality is the same scalar comparison a selector
// predicate uses (valuesEqual), so a case value and an `equals:` literal read the
// same way.
func matchCase(n *ir.Node, v any) string {
	for _, c := range n.Cases {
		if valuesEqual(v, c.Value) {
			return c.Then
		}
	}
	return n.Default
}

// onBranchRouted applies one branch's decision: it records the selected arm,
// closes the branch succeeded, and skips the arms it did not select.
//
// The decision is recorded BEFORE the branch terminates, because the branch's own
// termination is what releases arms that need it: an arm released into a
// still-undecided branch would be held by the driver and then have to be
// re-examined, and a decide-after-release order is the kind of ordering bug that
// only shows up in the graph where an arm sorts first.
func (r *Run) onBranchRouted(e BranchRoutedEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: branch_routed for unknown node %q", e.NodePath)
	}
	if st.State != StateReady {
		return fmt.Errorf("workflow/exec: branch_routed for node %s in state %s", e.NodePath, st.State)
	}
	n := r.byPath[e.NodePath]
	if e.Refusal != "" {
		return r.failTerminal(e.NodePath, e.Refusal)
	}
	if n == nil {
		return fmt.Errorf("workflow/exec: branch_routed for node %q that is not in the graph", e.NodePath)
	}
	selected := ""
	if e.Selected != "" {
		dep, ok := r.byID[e.Selected]
		if !ok {
			return r.failTerminal(e.NodePath, ReasonBranchSourceUnavailable+": "+n.Path+
				" routes to "+e.Selected+", which is not a step in this graph")
		}
		selected = dep.Path
	}
	st.Selected = selected
	if selected == "" {
		r.warnOnce("branch-nomatch:"+n.Path, n.Path+
			": the routed value matched no case and no default is declared; no arm runs on this path")
	}
	st.Result = routedLabel(selected)
	if n.Output != nil && n.Output.Name != "" {
		// A branch that declares an output records the arm it chose, so a
		// downstream emit can name the route without reading the event log.
		r.recordOutput(st, NodeOutput{NodePath: n.Path, Kind: n.Kind, Name: n.Output.Name, Value: st.Result})
	}
	if err := r.derive(NodeSucceededEvent{NodePath: n.Path, Outputs: outputNames(st)}); err != nil {
		return err
	}
	// Every arm the branch did not select is settled here, once, so a pending arm
	// cannot be left waiting for a release that will never come and a ready arm
	// cannot start on the strength of the path order.
	return r.skipUnselectedArms(n, selected)
}

// skipUnselectedArms applies a branch's decision to its arms.
func (r *Run) skipUnselectedArms(n *ir.Node, selected string) error {
	for _, id := range armIDs(n) {
		dep, ok := r.byID[id]
		if !ok || dep.Path == selected {
			continue
		}
		st := r.state[dep.Path]
		if st == nil || st.State.Terminal() {
			continue
		}
		if err := r.derive(NodeSkippedEvent{
			NodePath: dep.Path,
			Reason:   notTakenReason(n, n.Path, &NodeState{State: StateSucceeded, Selected: selected}),
		}); err != nil {
			return err
		}
	}
	return nil
}
