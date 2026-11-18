package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/registry"
	"github.com/buchenberg/yaah/internal/workflow/validate"
)

// call.go is the door half of a call node (yaah-0u0v.23): resolve the name
// against the registry the command composed, guard the depth, and run the
// child through the same host composition the root went through — fresh door,
// same runner, same retained runtime, same state store — so a nested run is a
// run, with its own budgets, its own trace, and its own durable record.
//
// A child composition is NOT a nested use of this Runtime: a run's wake
// channel and completion tally belong to one scheduler, and sharing them
// across two would let a nested run consume wakes meant for the outer one.
// Execute builds a Runtime per run for exactly this reason, and Call composes
// through it.
//
// Depth is the composition-scoped fact: the frame list travels on Options
// rather than the Runtime (one number, held by no run), and the guards read
// it before any child work starts — the validator's cross-document walk has
// already refused the cycles this guards against, so these are the
// defense-in-depth bound and the declared self-recursion bound, in that
// order.

// callFrameLimit is the absolute ceiling on nested workflow calls, aligned
// with the validator's MaxCallDepth: refuse before a child runs, whatever the
// documents declared.
const callFrameLimit = validate.MaxCallDepth

// Call executes one nested workflow.
func (rt *Runtime) Call(ctx context.Context, req exec.CallRequest) (exec.CallResult, error) {
	if rt.calls == nil {
		return exec.CallResult{}, errors.New("workflow/host: no subworkflow registry configured: a document with call nodes needs the --workflow-root its command was started with")
	}
	entry, ok := rt.calls.Lookup(req.Workflow)
	if !ok {
		// The registry memoizes; a name the analysis walked is already an
		// entry, so this load only happens for a caller that composed the
		// door without the analysis' walk. A registry that drifted between
		// validate and run answers the same way it does in the analysis:
		// what loads, runs — and the child's spec hash answer for what the
		// record holds on a resume.
		loaded, lerr := rt.calls.Load(req.Workflow)
		if lerr != nil {
			return exec.CallResult{}, fmt.Errorf("workflow/host: subworkflow %q does not load from the registry: %w", req.Workflow, lerr)
		}
		entry = loaded
	}
	if entry == nil || !entry.Loaded() {
		return exec.CallResult{}, fmt.Errorf("workflow/host: subworkflow %q is not loadable from the registry: %v", req.Workflow, entryLoadErr(entry))
	}
	frames := append(append([]string(nil), rt.callFrames...), req.Workflow)
	if len(frames) > callFrameLimit {
		return exec.CallResult{}, fmt.Errorf("workflow/host: call depth %d exceeds the ceiling %d: %s", len(frames), callFrameLimit, joinFrames(frames))
	}
	if copies := countFrames(frames, req.Workflow); copies > 1 {
		// The only legal recursion is a workflow re-entering itself, bounded
		// by the calling node's declaration: max_depth: b admits b traversals
		// of that edge, which is b copies beyond pass that is already
		// running — 1 + b frames of the same name. The validators' walk
		// prices the same number (validate.selfFactor); this guard holds it
		// at run time for the registry a second process may have changed.
		if allowed := req.MaxDepth + 1; copies > allowed {
			return exec.CallResult{}, fmt.Errorf("workflow/host: recursive call to %q would occupy frame %d of itself while its declared max_depth is %d: raise the declaration or bound the recursion at a shallower edge", req.Workflow, copies, req.MaxDepth)
		}
	}
	params, perr := callParams(entry.Graph, req.Params)
	if perr != nil {
		return exec.CallResult{}, perr
	}

	// The child runs under an id derived from the parent's and the node's
	// path, total and stateless, so the re-ask a resume makes resolves to
	// the same child record and continues it instead of re-running it.
	childID := exec.ChildRunID(req.ParentRunID, req.NodePath)
	opts := Options{
		Graph:        entry.Graph,
		RunID:        childID,
		Params:       params,
		Runner:       rt.Runner,
		Retained:     rt.Retained,
		ArtifactRoot: rt.artifactRoot,
		Store:        rt.runStore,
		SpecHash:     entry.SpecHash,
		SpecPath:     entry.Path,
		Now:          rt.Clock,
		Wait:         rt.Wait,
		Calls:        rt.calls,
		callFrames:   frames,
	}
	res, err := rt.runChild(ctx, opts, childID)
	if err != nil {
		return exec.CallResult{}, err
	}
	return childResult(childID, res), nil
}

// runChild executes one call's child run: resume the record when one exists —
// a mid-call crash left the child's dispatches committed and its budget spent,
// and re-running it would duplicate both — and fall back to a fresh Execute
// when the child never recorded state. A finished child is read back from its
// checkpoint rather than re-executed: its disposition is final, and the parent
// only needs the outcome it already reached.
func (rt *Runtime) runChild(ctx context.Context, opts Options, childID string) (Result, error) {
	if rt.runStore == nil {
		return Execute(ctx, opts)
	}
	res, err := Resume(ctx, opts, childID)
	switch {
	case err == nil:
		return res, nil
	case errors.Is(err, exec.ErrRunNotFound):
		return Execute(ctx, opts)
	case errors.Is(err, exec.ErrRunAlreadyFinished):
		return finishedChild(ctx, rt.runStore, childID)
	default:
		// A hash mismatch or a broken store refuses the call: the child the
		// registry holds now is not the document the record was built from,
		// and running either side of that disagreement would lie.
		return Result{}, err
	}
}

// finishedChild projects a child run whose record is final back into the shape
// Call folds, without re-executing anything.
func finishedChild(ctx context.Context, st exec.Store, childID string) (Result, error) {
	cp, err := st.LoadCheckpoint(ctx, childID)
	if err != nil {
		return Result{}, err
	}
	res := exec.Result{
		RunID:        cp.RunID,
		Status:       cp.Status,
		Reason:       cp.Reason,
		Dispatches:   cp.Dispatches,
		PeakInFlight: cp.PeakInFlight,
		Warnings:     append([]string(nil), cp.Warnings...),
		Nodes:        append([]*exec.NodeState(nil), cp.Nodes...),
	}
	for _, n := range res.Nodes {
		if n != nil && n.State == exec.StateSucceeded {
			res.Outputs = append(res.Outputs, n.Outputs...)
		}
	}
	return Result{Run: res}, nil
}

// childResult maps a child run's outcome onto the scheduler's envelope.
func childResult(childID string, res Result) exec.CallResult {
	out := exec.CallResult{
		Succeeded:  res.Run.Status == exec.RunSucceeded,
		ChildRunID: childID,
		Status:     res.Run.Status,
		Reason:     res.Run.Reason,
		Outputs:    map[string]string{},
		Warnings:   append([]string(nil), res.Warnings...),
	}
	out.Warnings = append(out.Warnings, res.Run.Warnings...)
	for _, o := range res.Run.Outputs {
		if o.Name != "" {
			out.Outputs[o.Name] = o.Value
		}
	}
	return out
}

// callParams checks a call's params against the workflow they are for. The
// scheduler renders them; the door owns the allowlist, because the registry
// is the trust boundary: a param the child never declared is either an
// authoring mistake or a free constant smuggled in under a name the child's
// templates did not ask for, and both are refused. Binding — defaults folding
// in, required params present — is exec.New's, exactly as for the root's
// --param values.
func callParams(g *ir.Graph, given map[string]any) (map[string]any, error) {
	for name := range given {
		if _, ok := g.Params[name]; !ok {
			return nil, fmt.Errorf("workflow/host: call parameter %q is not declared by subworkflow %q: the registry is an allowlist, and passing constants the child did not declare is refused", name, g.Name)
		}
	}
	if given == nil {
		return nil, nil
	}
	out := make(map[string]any, len(given))
	for k, v := range given {
		out[k] = v
	}
	return out, nil
}

func countFrames(frames []string, name string) int {
	n := 0
	for _, f := range frames {
		if f == name {
			n++
		}
	}
	return n
}

func joinFrames(frames []string) string {
	out := ""
	for i, f := range frames {
		if i > 0 {
			out += " -> "
		}
		out += f
	}
	return out
}

// entryLoadErr states the load failure a refused entry carries.
func entryLoadErr(e *registry.Entry) error {
	if e == nil {
		return nil
	}
	return e.Err
}
