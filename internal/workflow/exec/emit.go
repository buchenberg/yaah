package exec

import (
	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// ReasonEmitValueFailed prefixes an emit node whose declared value could not
// be rendered against the run's scope. The scope's own error names the cause —
// an unbound param, a step that never produced an output, or a `collect`
// reference this bead does not resolve — so the reason stays a quotation of
// the fence rather than a second opinion about it.
const ReasonEmitValueFailed = "emit_value_failed"

// settleValueStep is the whole of the emit binding: render the node's compiled
// value template against the scope snapshot taken at the moment the node became
// ready, record what it produced, and close the node as succeeded.
//
// Three properties keep this inside the scheduler core's contract. It leaves
// through no door: an emit dispatches nothing, so it consumes no attempt, no
// dispatch budget, and no slot under MaxConcurrentDispatches — which is what
// lets WF033's dispatch ceiling and this node agree. It parses nothing: the
// template was compiled into segments by ir.Resolve, and Render walks those
// segments, so an author's malformed reference is already a resolve failure
// (WF001) rather than a runtime surprise. And it writes nothing: the declared
// artifact path is recorded on the output binding and the host's dispatch
// adapter performs the write, restricted to the paths the document declared
// (internal/workflow/host). Recording instead of writing is also what keeps the
// event log a pure function of the graph and the scripted completions.
//
// The rendered text lands on NodeState.Result, so `steps.<id>.output` reads an
// emit's value the same way it reads a delegate's report — the scope fence in
// scope.go documents the value as the recorded result text, and an emit has no
// other result to record.
func (r *Run) settleValueStep(n *ir.Node, st *NodeState) error {
	var (
		text string
		err  error
	)
	if n.Value != nil {
		var res ir.RenderResult
		res, err = n.Value.Render(r.snapshot(n.Path, 1), ir.RenderOptions{})
		if err != nil {
			return r.failTerminal(n.Path, ReasonEmitValueFailed+": "+err.Error())
		}
		text = res.Text
	}
	if n.Value == nil && n.ArtifactPath == "" {
		// The step declared nothing to emit. It still terminates succeeded:
		// a later node that needs it must be released, and inventing a
		// failure for an empty declaration would strand the graph.
		return r.derive(NodeSucceededEvent{NodePath: n.Path})
	}
	st.Result = text
	r.recordOutput(st, NodeOutput{
		NodePath: n.Path,
		Kind:     n.Kind,
		Name:     emitBindingName(n),
		Value:    text,
		Artifact: n.ArtifactPath,
	})
	return r.derive(NodeSucceededEvent{NodePath: n.Path, Outputs: outputNames(st)})
}

// emitBindingName is the name an emit's value is bound under: the declared
// `output:` name when there is one, and otherwise the step id — the same name
// `steps.<id>.output` already reads. A body node with neither falls back to its
// path, so a recorded output always carries a string a trace can quote.
func emitBindingName(n *ir.Node) string {
	if n.Output != nil && n.Output.Name != "" {
		return n.Output.Name
	}
	if n.ID != "" {
		return n.ID
	}
	return n.Path
}

// recordOutput stores one output binding on a node's state, replacing an
// existing binding of the same name rather than appending to it. A retried
// node's output is its latest result, the same rule NodeState's last-completion
// fields follow; Attempt history keeps the sequence.
func (r *Run) recordOutput(st *NodeState, o NodeOutput) {
	for i, prev := range st.Outputs {
		if prev.Name == o.Name {
			st.Outputs[i] = o
			return
		}
	}
	st.Outputs = append(st.Outputs, o)
}

// outputNames lists a node's recorded bindings in declaration order for the
// node_succeeded log line. Names only: the log is the byte-identical artifact
// the determinism test compares, and a value's text belongs in Result.
func outputNames(st *NodeState) []string {
	if len(st.Outputs) == 0 {
		return nil
	}
	out := make([]string, 0, len(st.Outputs))
	for _, o := range st.Outputs {
		out = append(out, o.Name)
	}
	return out
}
