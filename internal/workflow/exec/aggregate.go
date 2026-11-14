package exec

import (
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Aggregate execution (yaah-0u0v.15).
//
// An aggregate combines the values its `inputs` selectors resolve into one value.
// Two of the three strategies are deterministic Go functions over those inputs and
// dispatch nothing — concat and json_merge are value steps, like an emit — and one
// is a dispatch: summarize runs the aggregate's BODY through the door, because
// condensing N results into one is a model's job and the graph says so by
// declaring a body.
//
// # Where a summarize's role comes from
//
// From the body. ir.resolveAggregate resolves the node's `body` into graph nodes
// and the kind's payload is inputs|strategy|body (see resolve.go and
// validate/valuepayload.go), so a summarize aggregate's role, prompt, lane,
// provider, grants and model are the body delegate's. The aggregate node carries
// no role of its own, and looking for one here would be inventing a second place
// to declare it.
//
// # Cost
//
// A deterministic combine consumes no dispatch, no attempt, and no slot under
// MaxConcurrentDispatches: it touches nothing outside the reducer. A summarize
// dispatch is an ordinary attempt — counted in the run's dispatch tally, against
// the concurrency cap, and against the budget axes — which is why the aggregate's
// body instances are instantiated as ordinary nodes rather than run inline.
//
// # What the inputs are for
//
// For concat and json_merge the inputs ARE the value: they are resolved once, in
// declaration order, and combined. For summarize they are resolved and checked —
// an unreadable input fails the aggregate before anything dispatches — and the
// resolved list is what the aggregate's own collect records, because the v1
// container vocabulary mints no binding for a container's inputs (ir.resolveBody
// gives an aggregate body neither `as` nor `index`), so a summarizer reads the
// steps it summarizes through the ordinary `steps.<id>.output` references.

// Reasons the aggregate family states.
const (
	// ReasonAggregateInputsEmpty prefixes an aggregate that declares no inputs:
	// there is nothing to combine, and an empty combined value would look like a
	// successful combine of nothing.
	ReasonAggregateInputsEmpty = "aggregate_inputs_empty"
	// ReasonAggregateInputUnavailable prefixes an input the run cannot read: no
	// source, a producer that did not succeed, or a field the value does not
	// carry.
	ReasonAggregateInputUnavailable = "aggregate_input_unavailable"
	// ReasonAggregateStrategyUnsupported names a strategy outside the three the
	// vocabulary declares.
	ReasonAggregateStrategyUnsupported = "aggregate_strategy_unsupported"
	// ReasonAggregateBodyMissing prefixes a summarize with no body to dispatch.
	ReasonAggregateBodyMissing = "aggregate_body_missing"
	// ReasonAggregateBodyUnresolved prefixes a body node whose need names nothing
	// in the aggregate's scope.
	ReasonAggregateBodyUnresolved = "aggregate_body_unresolved"
	// ReasonAggregateBodyFailed names a summarize whose body failed.
	ReasonAggregateBodyFailed = "aggregate_body_failed"
	// ReasonAggregateCombineFailed prefixes a combine that could not be performed:
	// an input of the wrong shape for the strategy, or a value that would not
	// encode as the node's recorded text.
	ReasonAggregateCombineFailed = "aggregate_combine_failed"
)

// aggregateStepOnce drives one aggregate cycle: the first ready aggregate whose
// inputs can be read gets them resolved. An aggregate whose input names a producer
// that has not terminated is a WAIT, not an empty input list, so the cycle falls
// through and the run does whatever else it can.
func (r *Run) aggregateStepOnce() (bool, error) {
	for _, p := range r.ready {
		n := r.byPath[p]
		if n == nil || n.Kind != ir.KindAggregate {
			continue
		}
		st := r.state[p]
		if st == nil || st.State != StateReady {
			continue
		}
		if len(r.instanceOf[p]) > 0 {
			// The combine body is already running; its completion closes the
			// aggregate (noteAggregateInstance).
			continue
		}
		advanced, err := r.aggregateStep(n)
		if err != nil {
			return true, err
		}
		if advanced {
			return true, nil
		}
	}
	return false, nil
}

// aggregateStep resolves one aggregate's inputs and submits them. The reducer
// decides what they mean: a deterministic strategy combines them there, and a
// summarize instantiates its body there.
func (r *Run) aggregateStep(n *ir.Node) (bool, error) {
	if len(n.Inputs) == 0 {
		return true, r.Apply(AggregateInputsEvent{
			NodePath: n.Path,
			Refusal:  ReasonAggregateInputsEmpty + ": " + n.Path + " declares no inputs to combine",
		})
	}
	values := make([]any, 0, len(n.Inputs))
	for _, sel := range n.Inputs {
		if sel == nil || sel.From == nil {
			return true, r.Apply(AggregateInputsEvent{
				NodePath: n.Path,
				Refusal:  ReasonAggregateInputUnavailable + ": " + n.Path + " declares an input with no source",
			})
		}
		if p := r.awaitingProducer(sel); p != "" {
			return false, nil
		}
		v, err := r.selectorValue(sel)
		if err != nil {
			return true, r.Apply(AggregateInputsEvent{
				NodePath: n.Path,
				Refusal:  ReasonAggregateInputUnavailable + ": " + err.Error(),
			})
		}
		if sel.AsList {
			v = []any{v}
		}
		values = append(values, v)
	}
	return true, r.Apply(AggregateInputsEvent{NodePath: n.Path, Inputs: values})
}

// onAggregateInputs folds one aggregate's resolved inputs. A deterministic
// strategy finishes the node here — it dispatches nothing, so the combine is a
// value step in the reducer like an emit's render — and summarize instantiates the
// body it declared, which the ordinary scheduler then runs.
func (r *Run) onAggregateInputs(e AggregateInputsEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: aggregate_inputs for unknown node %q", e.NodePath)
	}
	if st.State != StateReady {
		return fmt.Errorf("workflow/exec: aggregate_inputs for node %s in state %s", e.NodePath, st.State)
	}
	if e.Refusal != "" {
		return r.failTerminal(e.NodePath, e.Refusal)
	}
	n := r.byPath[e.NodePath]
	if n == nil {
		return fmt.Errorf("workflow/exec: aggregate_inputs for node %q that is not in the graph", e.NodePath)
	}
	if n.Strategy == ir.AggregateSummarize {
		return r.instantiateCombineBody(n, st)
	}
	combined, refusal := combine(n.Strategy, e.Inputs)
	if refusal != "" {
		return r.failTerminal(e.NodePath, refusal)
	}
	return r.closeAggregate(n, st, combined)
}

// combine applies one deterministic aggregate strategy to the resolved inputs, in
// declaration order.
//
// concat spreads a list input's elements and contributes any other value as one
// element, so `[a, b]` and `a` combine to `[a, b]` either way and a scalar input is
// never silently dropped. json_merge merges mapping inputs key by key; a later
// input's key replaces an earlier one's, which is the only rule that does not need
// a diff to explain itself, and encoding/json sorts the keys, so the recorded text
// is a function of the inputs alone.
func combine(s ir.AggregateStrategy, inputs []any) (any, string) {
	switch s {
	case ir.AggregateConcat:
		out := make([]any, 0, len(inputs))
		for _, v := range inputs {
			elems, isList := listValue(v)
			if !isList {
				out = append(out, v)
				continue
			}
			out = append(out, elems...)
		}
		return out, ""
	case ir.AggregateJSONMerge:
		merged := map[string]any{}
		for i, v := range inputs {
			m := objectOf(v)
			if m == nil {
				return nil, ReasonAggregateCombineFailed + fmt.Sprintf(
					": inputs[%d] is %T, which is not a mapping json_merge can merge", i, v)
			}
			for k, val := range m {
				merged[k] = val
			}
		}
		return merged, ""
	case ir.AggregateSummarize:
		// A summarize is a dispatch, never a combine: reaching here means the
		// reducer dispatched on the strategy twice.
		return nil, ReasonAggregateCombineFailed + ": strategy summarize is a dispatch, not a combine"
	}
	return nil, ReasonAggregateStrategyUnsupported + ": " + aggregateStrategyLabel(s)
}

// aggregateStrategyLabel renders a strategy for a reason line, so an undeclared
// one reads as "no strategy" instead of an empty quote.
func aggregateStrategyLabel(s ir.AggregateStrategy) string {
	if s == "" {
		return "no strategy declared; the vocabulary is concat|summarize|json_merge"
	}
	return string(s) + "; the vocabulary is concat|summarize|json_merge"
}

// instantiateCombineBody starts a summarize aggregate's body as one execution of
// its body nodes: the same instantiation a fan-out item and a loop iteration get,
// with the body's own template paths because there is exactly one execution of it.
func (r *Run) instantiateCombineBody(n *ir.Node, st *NodeState) error {
	if len(n.Body) == 0 {
		return r.failTerminal(n.Path, ReasonAggregateBodyMissing+": "+n.Path+
			" declares strategy: summarize with no body to dispatch")
	}
	paths, unresolved, err := r.instantiateBody(n, instancePlan{
		path: func(b string) string { return b },
		item: 0,
	})
	if err != nil {
		return err
	}
	if unresolved != "" {
		return r.failTerminal(n.Path, ReasonAggregateBodyUnresolved+": "+unresolved+
			" needs a step that is not in the aggregate's scope")
	}
	return r.readyEntries(paths)
}

// noteAggregateInstance closes a summarize aggregate when its body has finished:
// the body's product becomes the aggregate's value, which is the whole point of
// dispatching it. A failed body fails the aggregate through the aggregate's own
// on_failure.
func (r *Run) noteAggregateInstance(n *ir.Node, st *NodeState) error {
	paths := r.instanceOf[n.Path]
	if len(paths) == 0 || !r.scopeTerminal(paths) {
		return nil
	}
	state, reason, value := r.foldScope(paths)
	if state != StateSucceeded {
		return r.failTerminal(n.Path, ReasonAggregateBodyFailed+": the combine body ended "+string(state)+": "+reason)
	}
	return r.closeAggregate(n, st, value)
}

// closeAggregate records an aggregate's combined value and closes it. The value is
// kept structured on the node (NodeState.Combined), so `steps.<id>.output` and a
// selector read the list or the mapping rather than a re-parse of its text, while
// Result carries the text a trace and an emit quote.
func (r *Run) closeAggregate(n *ir.Node, st *NodeState, value any) error {
	text, err := recordedValue(value)
	if err != nil {
		return r.failTerminal(n.Path, ReasonAggregateCombineFailed+": encoding the combined value: "+err.Error())
	}
	st.Combined = value
	st.Result = text
	r.recordOutput(st, NodeOutput{
		NodePath: n.Path,
		Kind:     n.Kind,
		Name:     aggregateBindingName(n),
		Value:    text,
	})
	return r.derive(NodeSucceededEvent{NodePath: n.Path, Outputs: outputNames(st)})
}

// recordedValue renders one combined value as a node's recorded text. A string
// arrives as itself — the way a delegate's delivered report does — and anything
// structured is encoded, so a consumer of the text never sees Go's own formatting
// of a list.
func recordedValue(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// aggregateBindingName is the name an aggregate's value is bound under: its
// declared output name, else its step id, else its path — the same rule emit,
// settle, fan-out, and loop follow.
func aggregateBindingName(n *ir.Node) string {
	if n.Output != nil && n.Output.Name != "" {
		return n.Output.Name
	}
	if n.ID != "" {
		return n.ID
	}
	return n.Path
}
