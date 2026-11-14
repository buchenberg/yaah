package exec

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Loop execution (yaah-0u0v.15).
//
// A loop is a container that runs its body more than once: iteration k+1 starts
// only once every instance of iteration k has terminated, and the run stops when
// the declared terminator (`until`) matches or when the declared bound
// (`max_iterations`) is reached. It is the one container that is SEQUENTIAL by
// construction — not by a cap, but because the next iteration's work is defined
// by the previous one's outcome — so a loop never has two iterations' worth of
// dispatches in flight, while the dispatches it does make are ordinary nodes
// counted against MaxConcurrentDispatches and the budget axes like any other.
//
// # Iteration paths
//
// The convention is the fan-out's, one word different: a loop numbers ITERATIONS
// where a fan-out numbers items.
//
//	steps.repair                  the loop node itself
//	steps.repair.iters[1]         the second iteration — a path PREFIX, not a node
//	steps.repair.iters[1].body[0] the iteration's first body node, instantiated
//
// It is (a) deterministic — the iteration index is the run's own counter, so the
// same graph always mints the same paths in the same order; (b) collision-free —
// nothing authored can contain ".iters[", because ir.Resolve only appends
// ".body[i]"; (c) greppable — one prefix selects one iteration and a dispatch id
// ("steps.repair.iters[1].body[0]#1") names the attempt absolutely; and (d)
// iteration-ordered, which is also the order the collect accumulates in. A loop
// nested in a fan-out chains the same way:
// steps.outer.items[0].body[1].iters[2].body[0].
//
// # The collect
//
// `collect` accumulates one value per SUCCEEDED iteration, in iteration order:
// the iteration's product — its last body node's recorded value, exactly as a
// fan-out item's product is — because a body is ordered as a sequence (§5.3) and
// its earlier nodes are inputs to the last. A skipped iteration contributes
// nothing and still counts as an iteration: the author declared the skip, and the
// run says so through the iteration ledger rather than by shortening the count.
//
// # What ends a loop
//
// Three answers, and the difference between them is what the author declared:
//
//   - `until` matched: the loop did what it was asked for. It closes succeeded
//     with the accumulation so far.
//   - no `until` declared: the bound is the whole declaration, so the loop runs
//     exactly max_iterations iterations and closes succeeded.
//   - `until` declared and never matched by the bound: the loop ran its ceiling
//     without reaching the condition it declared, so it fails with
//     loop_iterations_exhausted and its own on_failure decides what that means for
//     the run. Reporting success here would claim the repair converged when the
//     terminator says it never did.
//
// # Failure
//
// A body instance that fails fails the LOOP, through the loop's on_failure — the
// same rule a fan-out item's failure follows, for the same reason: the iteration
// is one of N and whether N-1 still count is the container's declaration, so the
// body node's own on_failure still applies to the instance first, and a skipped
// instance is not a failure at all.

// Reasons the loop family states.
const (
	// ReasonLoopBodyMissing prefixes a loop with no body to iterate.
	ReasonLoopBodyMissing = "loop_body_missing"
	// ReasonLoopUnbounded prefixes a loop that declares no max_iterations. A
	// deterministic core that invented a bound would be inventing a host policy
	// nobody authored, and one that iterated without a ceiling would not
	// terminate; WF034 reports the declaration and the run refuses instead.
	ReasonLoopUnbounded = "loop_unbounded"
	// ReasonLoopUntilUnavailable prefixes a terminator the run cannot evaluate:
	// no source, a producer that did not succeed, a field the collected value
	// does not carry, or a read of the loop's own output. Fanning out to "the
	// condition never matched" on any of those would run the loop to its bound
	// and call the ceiling an answer.
	ReasonLoopUntilUnavailable = "loop_until_unavailable"
	// ReasonLoopBodyUnresolved prefixes a body node whose need names nothing in
	// its own iteration scope. Such an instance could never become ready, so the
	// loop would stay open forever; the iteration refuses instead.
	ReasonLoopBodyUnresolved = "loop_body_unresolved"
	// ReasonLoopIterationFailed names the iteration behind a failed loop.
	ReasonLoopIterationFailed = "loop_iteration_failed"
	// ReasonLoopIterationsExhausted names a loop whose declared terminator never
	// matched within its declared bound.
	ReasonLoopIterationsExhausted = "loop_iterations_exhausted"
	// ReasonLoopCollectFailed prefixes an accumulation that could not be encoded
	// as the node's recorded text.
	ReasonLoopCollectFailed = "loop_collect_failed"
)

// LoopIteration is one iteration of a loop node: the body instances it became and
// its own outcome. It is the ledger a trace reads and the record the accumulated
// collect is assembled from, so it is written only inside Apply, like every other
// piece of lifecycle state.
type LoopIteration struct {
	// Index is the iteration's position in the loop, 0-based.
	Index int `json:"index"`
	// Paths are the iteration's body instances in body order. The last one is
	// the iteration's PRODUCT: its recorded value is what the iteration
	// contributes to the collect.
	Paths []string `json:"paths,omitempty"`
	// State is the iteration's outcome, folded from its instances once they all
	// terminated: failed outranks cancelled outranks skipped, and success means
	// the body ran to its product.
	State State `json:"state,omitempty"`
	// Reason names the instance that decided the outcome, with its own reason.
	Reason string `json:"reason,omitempty"`
	// Value is the iteration's product value, nil for an iteration that did not
	// succeed.
	Value any `json:"value,omitempty"`
}

// iterationPath mints one loop iteration's instance path: the body node's own
// path with the iteration discriminator inserted between its container and its
// body index.
func iterationPath(containerPath string, iteration int, bodyPath string) string {
	return instanceChildPath(containerPath, ".iters["+strconv.Itoa(iteration)+"]", bodyPath)
}

// maxIterationsOf reads a loop's declared bound. Only a resolved graph that
// skipped classifyKind's refusal reaches it with no bound, and the caller states
// that as the loop's failure.
func maxIterationsOf(n *ir.Node) int {
	if n == nil || n.MaxIterations == nil || *n.MaxIterations < 1 {
		return 0
	}
	return *n.MaxIterations
}

// loopStepOnce drives one loop cycle: it starts the first iteration, starts the
// next one, or closes the loop on its terminator or its bound. A loop whose
// current iteration is still running is a WAIT, so the scan falls through and the
// run does whatever else it can — the same rule a fan-out whose `over` producer
// has not terminated follows.
func (r *Run) loopStepOnce() (bool, error) {
	for _, p := range r.ready {
		n := r.byPath[p]
		if n == nil || n.Kind != ir.KindLoop {
			continue
		}
		st := r.state[p]
		if st == nil || st.State != StateReady {
			continue
		}
		advanced, err := r.loopStep(n, st)
		if err != nil {
			return true, err
		}
		if advanced {
			return true, nil
		}
	}
	return false, nil
}

// loopStep performs one loop cycle for one ready loop node, and submits the
// resulting event. It mutates nothing itself: both starting an iteration and
// closing the loop are decided inside Apply.
func (r *Run) loopStep(n *ir.Node, st *NodeState) (bool, error) {
	// The current iteration owns the loop until it terminates. "Not finished yet"
	// is a wait, not an empty run: asking for the next iteration here would put
	// two iterations' work in flight and make the accumulation order depend on
	// the schedule.
	if len(st.Iterations) > 0 {
		cur := st.Iterations[len(st.Iterations)-1]
		if !r.scopeTerminal(cur.Paths) {
			return false, nil
		}
		matched, deferred, refusal := r.loopUntil(n, st)
		if deferred {
			return false, nil
		}
		switch {
		case refusal != "":
			return true, r.Apply(LoopCompletedEvent{NodePath: n.Path, Iterations: len(st.Iterations), Refusal: refusal})
		case matched:
			return true, r.Apply(LoopCompletedEvent{
				NodePath: n.Path, Iterations: len(st.Iterations), Terminated: LoopTerminatedUntil,
			})
		case len(st.Iterations) >= maxIterationsOf(n):
			return true, r.Apply(LoopCompletedEvent{
				NodePath: n.Path, Iterations: len(st.Iterations), Terminated: LoopTerminatedBound, Exhausted: true,
			})
		}
	}
	return true, r.Apply(LoopIterationStartedEvent{NodePath: n.Path, Iteration: len(st.Iterations)})
}

// Loop terminator words, carried on LoopCompletedEvent so the event log says
// which declaration ended the loop.
const (
	// LoopTerminatedUntil: the declared terminator matched.
	LoopTerminatedUntil = "until"
	// LoopTerminatedBound: the declared max_iterations was reached.
	LoopTerminatedBound = "bound"
)

// loopUntil evaluates a loop's terminator against recorded run state, and answers
// three things at once: whether the condition holds, whether the run must WAIT
// for a producer that has not terminated, and whether the declaration is one this
// run can evaluate at all.
//
// It reads through the same value extraction a template reads (selectorValue), so
// `until: {from: steps.x.collect}` and `{{ steps.x.collect }}` cannot disagree
// about what a step produced — with the one exception the loop owns: a terminator
// may read the loop's OWN collect, which is the accumulation so far. WF013
// permits exactly that read (a loop terminator reading its own collect is not
// recursion), and it has to be permitted, because a terminator that could only be
// evaluated after the loop closed would be useless.
func (r *Run) loopUntil(n *ir.Node, st *NodeState) (matched, deferred bool, refusal string) {
	sel := n.Until
	if sel == nil || sel.From == nil {
		// No terminator declared: the bound is the whole declaration, so nothing
		// matches early and the loop runs exactly max_iterations iterations.
		return false, false, ""
	}
	if self, ok := r.loopSelfSource(n, sel); ok {
		if !self {
			return false, false, ReasonLoopUntilUnavailable + ": " + sel.From.String() +
				" reads the loop's own output, which does not exist while it iterates; read " + n.Path + ".collect"
		}
		ok, why := selectorSatisfied(st.collectValue(), sel.Predicate)
		if why != "" {
			return false, false, ReasonLoopUntilUnavailable + ": " + sel.From.String() + " " + why
		}
		return ok, false, ""
	}
	if p := r.awaitingProducer(sel); p != "" {
		return false, true, ""
	}
	v, err := r.selectorValue(sel)
	if err != nil {
		return false, false, ReasonLoopUntilUnavailable + ": " + err.Error()
	}
	if sel.AsList {
		v = []any{v}
	}
	ok, why := selectorSatisfied(v, sel.Predicate)
	if why != "" {
		return false, false, ReasonLoopUntilUnavailable + ": " + sel.From.String() + " " + why
	}
	return ok, false, ""
}

// loopSelfSource reports whether a terminator reads the loop node itself, and if
// so whether it reads the accumulation (the collect) rather than the node's
// output.
func (r *Run) loopSelfSource(n *ir.Node, sel *ir.Selector) (collectRead, self bool) {
	src := sel.From
	if src.Kind != ir.SourceStepOutput && src.Kind != ir.SourceStepCollect {
		return false, false
	}
	dep, ok := r.byID[src.StepID]
	if !ok || dep.Path != n.Path {
		return false, false
	}
	return src.Kind == ir.SourceStepCollect, true
}

// selectorSatisfied answers whether a resolved value satisfies a terminator's
// predicate, and is the one predicate reading a terminator and a routed value
// share.
//
// A LIST is tested element-wise: the predicate keeps the elements whose `field`
// matches and the value satisfies it when the kept set is not empty. That is the
// reading the selector vocabulary has everywhere else (`over`, `where`), and it
// makes a terminator MONOTONE — an accumulation that satisfied it once cannot
// stop satisfying it, so "until" cannot flip back to false as more iterations
// land. A scalar is tested directly, and no predicate at all means "is there
// anything there": a non-empty list, or a value that is neither nil nor empty.
func selectorSatisfied(v any, p ir.Predicate) (bool, string) {
	elems, isList := listValue(v)
	if !isList {
		if p.Op == ir.PredNone && p.Field == "" {
			return !valueEmpty(v), ""
		}
		ok, err := predicateMatch(v, p)
		if err != nil {
			return false, err.Error()
		}
		return ok, ""
	}
	if p.Op == ir.PredNone && p.Field == "" {
		return len(elems) > 0, ""
	}
	for _, e := range elems {
		field, why := elementField(e, p.Field)
		if why != "" {
			return false, why
		}
		ok, err := predicateMatch(field, p)
		if err != nil {
			return false, err.Error()
		}
		if ok {
			return true, ""
		}
	}
	return false, ""
}

// listValue reads a resolved value as a list, for the element-wise readings.
func listValue(v any) ([]any, bool) {
	switch t := v.(type) {
	case []any:
		return t, true
	case []string:
		out := make([]any, 0, len(t))
		for _, s := range t {
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// onLoopIterationStarted instantiates one iteration's body instances and hands
// them to the ordinary scheduler: the ready queue, join-all over needs, the
// dispatch cap, budget accounting. The iteration index is checked against the
// ledger rather than trusted, because the driver's counter and the reducer's are
// the same sequence or the run has a bug.
func (r *Run) onLoopIterationStarted(e LoopIterationStartedEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: loop_iteration_started for unknown node %q", e.NodePath)
	}
	if st.State != StateReady {
		return fmt.Errorf("workflow/exec: loop_iteration_started for node %s in state %s", e.NodePath, st.State)
	}
	n := r.byPath[e.NodePath]
	if n == nil || len(n.Body) == 0 {
		return r.failTerminal(e.NodePath, ReasonLoopBodyMissing+": "+e.NodePath+" declares no body to iterate")
	}
	if e.Iteration != len(st.Iterations) {
		return fmt.Errorf("workflow/exec: loop %s asked for iteration %d but %d iteration(s) exist",
			e.NodePath, e.Iteration, len(st.Iterations))
	}
	if len(st.Iterations) > 0 {
		cur := st.Iterations[len(st.Iterations)-1]
		if !r.scopeTerminal(cur.Paths) {
			return fmt.Errorf("workflow/exec: loop %s cannot start iteration %d before iteration %d terminated",
				e.NodePath, e.Iteration, cur.Index)
		}
	}
	paths, unresolved, err := r.instantiateBody(n, instancePlan{
		path: func(b string) string { return iterationPath(n.Path, e.Iteration, b) },
		item: e.Iteration,
	})
	if err != nil {
		return err
	}
	st.Iterations = append(st.Iterations, LoopIteration{
		Index: e.Iteration,
		Paths: paths,
		State: StatePending,
	})
	if unresolved != "" {
		return r.failTerminal(n.Path, ReasonLoopBodyUnresolved+": "+unresolved+
			" needs a step that is not in its iteration scope")
	}
	return r.readyEntries(paths)
}

// noteLoopInstance is the loop's reaction to one of its instances terminating:
// when the last instance of the current iteration is done, the iteration is folded
// into the loop's ledger and its product joins the accumulation. A failed
// iteration fails the loop through the loop's on_failure.
//
// The loop itself stays ready either way. Whether the terminator matched, the
// bound was reached, or another iteration starts is the driver's decision, made
// where the selector can be evaluated against recorded state — and it is made
// inside Apply from the resulting event, so the loop's transitions happen in one
// place.
func (r *Run) noteLoopInstance(n *ir.Node, st *NodeState, inst *instance) error {
	if len(st.Iterations) == 0 {
		return nil
	}
	cur := &st.Iterations[len(st.Iterations)-1]
	if cur.Index != inst.item || cur.State != StatePending || !r.scopeTerminal(cur.Paths) {
		return nil
	}
	state, reason, value := r.foldScope(cur.Paths)
	cur.State, cur.Reason, cur.Value = state, reason, value
	if state == StateSucceeded {
		st.Collect = append(st.Collect, value)
	}
	if state != StateFailed {
		return nil
	}
	return r.failTerminal(n.Path, ReasonLoopIterationFailed+": iteration "+strconv.Itoa(cur.Index)+" "+cur.Reason)
}

// onLoopCompleted closes a loop: with the accumulation it reached, or with the
// failure its declaration earned. Exhaustion is a failure only when a terminator
// was declared — without one the bound IS the declaration, so reaching it is the
// loop doing exactly what it was asked for.
func (r *Run) onLoopCompleted(e LoopCompletedEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: loop_completed for unknown node %q", e.NodePath)
	}
	if st.State != StateReady {
		return fmt.Errorf("workflow/exec: loop_completed for node %s in state %s", e.NodePath, st.State)
	}
	n := r.byPath[e.NodePath]
	if e.Refusal != "" {
		return r.failTerminal(e.NodePath, e.Refusal)
	}
	if n == nil {
		return fmt.Errorf("workflow/exec: loop_completed for node %q that is not in the graph", e.NodePath)
	}
	if e.Exhausted && n.Until != nil {
		return r.failTerminal(e.NodePath, fmt.Sprintf(
			"%s: %s: until never matched within max_iterations=%d (%d iteration(s) ran)",
			ReasonLoopIterationsExhausted, e.NodePath, maxIterationsOf(n), len(st.Iterations)))
	}
	return r.closeLoop(n, st)
}

// closeLoop records the accumulated value and closes the loop. The value is the
// collect list, so `steps.<id>.collect` and `steps.<id>.output` are two readings
// of one list — and an empty accumulate reports [] rather than nil, because "the
// loop ran and produced nothing" is a fact and a template rendering it should see
// an empty list, not a missing value.
func (r *Run) closeLoop(n *ir.Node, st *NodeState) error {
	collect := st.collectValue()
	st.Collect = collect
	encoded, err := json.Marshal(collect)
	if err != nil {
		return r.failTerminal(st.Path, ReasonLoopCollectFailed+": encoding the accumulated value: "+err.Error())
	}
	st.Result = string(encoded)
	r.recordOutput(st, NodeOutput{
		NodePath: n.Path,
		Kind:     n.Kind,
		Name:     loopBindingName(n),
		Value:    st.Result,
	})
	return r.derive(NodeSucceededEvent{NodePath: n.Path, Outputs: outputNames(st)})
}

// loopBindingName is the name a loop's accumulated value is bound under: its
// declared `collect` name, else its step id, else its path — the same rule emit,
// settle, and fan-out follow, so `steps.<id>.output` and a named binding read one
// value.
func loopBindingName(n *ir.Node) string {
	if n.Collect != "" {
		return n.Collect
	}
	if n.ID != "" {
		return n.ID
	}
	return n.Path
}
