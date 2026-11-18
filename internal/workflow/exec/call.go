package exec

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// call.go is the call door (yaah-0u0v.23): one ready call node per scheduler
// cycle, asked to the Runtime synchronously, folded from the outcome the same
// way every other envelope is.
//
// The step owns exactly two judgements, and both are run-state readings rather
// than graph facts: whether the node's params render against the current
// scope, and how the child's outcome maps onto the node's disposition. Name
// resolution, the registry, budgets for the child run, and the depth guards
// are the door's — see internal/workflow/host — and the validators own the
// declaration facts (unknown names, cycles, unbounded depth) before a run
// starts. Like a settlement, a call consumes no dispatch budget and holds no
// slot under MaxConcurrentDispatches: the child prices its own dispatches
// against the document the child is, which is the only budgeting it declared.
//
// Persistence follows the settle pattern's opposite half: nothing is recorded
// before the door is asked, because the child run's own record IS the
// write-ahead — a crash mid-call leaves the node ready in the parent's
// checkpoint and the child's dispatches committed in the child's, so a resume
// re-asks and the door resumes the child from its record rather than
// re-running it.

// ChildRunID derives the durable identity of a call's child run from the
// parent's run id and the call node's path. The derivation is total and holds
// no state, which is the whole idempotency story: any process that resumes
// the same parent run and reaches the same call node computes the same id,
// finds the child's record, and continues the child where it stopped instead
// of running it again.
func ChildRunID(parentRunID, nodePath string) string {
	return parentRunID + "!" + nodePath
}

// callStepOnce drives one call cycle: the driver finds the first ready call
// node, asks the door for the nested run, and submits the resulting event. It
// mutates nothing itself, which is why a scripted fake and the real composed
// runtime produce the same sequence.
func (r *Run) callStepOnce(ctx context.Context) (bool, error) {
	path, ok := r.nextCallPath()
	if !ok {
		return false, nil
	}
	return r.callStep(ctx, path)
}

// nextCallPath returns the first ready call node in the ready queue's
// canonical order. The queue holds nodes of every kind, so the walk skips
// what is not a call — a call that is not ready is waiting on a need, and a
// call whose state is terminal never returns here.
func (r *Run) nextCallPath() (string, bool) {
	for _, p := range r.ready {
		n := r.byPath[p]
		if n == nil || n.Kind != ir.KindCall {
			continue
		}
		st := r.state[p]
		if st.State != StateReady {
			continue
		}
		return p, true
	}
	return "", false
}

// callStep performs one nested workflow call: render the node's params
// against the run's scope, hand them to the door, and fold the child's
// outcome.
func (r *Run) callStep(ctx context.Context, path string) (bool, error) {
	n := r.byPath[path]
	st := r.state[path]
	if n == nil || st == nil {
		return false, nil
	}
	params, rerr := renderCallParams(n, r.snapshot(path, 1))
	if rerr != nil {
		return true, r.failTerminal(path, ReasonCallFailed+": "+rerr.Error())
	}
	req := CallRequest{
		Node:        n,
		Workflow:    n.Workflow,
		MaxDepth:    callDepthOf(n),
		Params:      params,
		ParentRunID: r.runID,
		NodePath:    path,
	}
	res, err := r.rt.Call(ctx, req)
	if err != nil {
		// A door error means the request never produced an answer — no
		// registry, a depth guard refusal, a store that would not open. The
		// child's own failures are not errors here: they travel on CallResult
		// and the fold below states them.
		return true, r.failTerminal(path, ReasonRuntimeError+": Runtime.Call: "+err.Error())
	}
	if !res.Succeeded {
		// The child ran even though it did not succeed — its own records are
		// the proof of what it did — so the node records the same id it would
		// on success: the parent's trace points at the child's records either
		// way (yaah-0u0v.25), and a failed child is not an unmentioned child.
		st.CallRunID = res.ChildRunID
		reason := ReasonCallFailed + ": subworkflow " + n.Workflow + " (run " + res.ChildRunID + ")"
		if res.Reason != "" {
			reason += " — " + res.Reason
		}
		return true, r.failTerminal(path, reason)
	}
	// The child's bindings are the node's value: a map under the output name,
	// recorded as JSON the same way a structured report is, so a template
	// reads one binding (`steps.<id>.output.summary`) and a selector can walk
	// into it without this package inventing a second value shape.
	names := make([]string, 0, len(res.Outputs))
	out := make(map[string]string, len(res.Outputs))
	for k, v := range res.Outputs {
		names = append(names, k)
		out[k] = v
	}
	sort.Strings(names)
	text, merr := marshalCallOutputs(out)
	if merr != nil {
		// json.Marshal of map[string]string cannot fail; this arm exists so
		// the fold never depends on that proof staying true.
		return true, r.failTerminal(path, ReasonRuntimeError+": Runtime.Call: "+merr.Error())
	}
	st.Result = text
	r.recordOutput(st, NodeOutput{
		NodePath: n.Path,
		Kind:     n.Kind,
		Name:     emitBindingName(n),
		Value:    text,
	})
	st.CallRunID = res.ChildRunID
	return true, r.derive(NodeSucceededEvent{NodePath: n.Path, Outputs: outputNames(st)})
}

// renderCallParams resolves the node's call params against the scope: string
// templates render to text, and every other value passes through as the
// constant the author wrote. Order is the node's map order, so two runs over
// one graph agree.
func renderCallParams(n *ir.Node, scope ir.Scope) (map[string]any, error) {
	if len(n.CallParams) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(n.CallParams))
	for k, pv := range n.CallParams {
		if pv.Template != nil {
			res, err := pv.Template.Render(scope, ir.RenderOptions{})
			if err != nil {
				return nil, err
			}
			out[k] = res.Text
			continue
		}
		out[k] = pv.Const
	}
	return out, nil
}

func callDepthOf(n *ir.Node) int {
	if n == nil || n.MaxDepth == nil || *n.MaxDepth < 1 {
		return 0
	}
	return *n.MaxDepth
}

func marshalCallOutputs(out map[string]string) (string, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
