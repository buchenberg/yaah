package exec

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// callYAML is one call fed by a delegate and feeding an emit: the shape that
// exercises render-params, the door ask, the outcome fold, and the scope's
// reading of the child's bindings in one document.
const callYAML = `apiVersion: yaah.workflow/v1
kind: Workflow
metadata:
  name: caller
spec:
  steps:
    - {id: plan, kind: delegate, role: developer, prompt: plan it}
    - id: sub
      kind: call
      workflow: helper
      needs: [plan]
      params: {task: "for {{ steps.plan.output }}"}
    - id: report
      kind: emit
      needs: [sub]
      value: "child said: {{ steps.sub.output.summary }}"
`

// runCallGraph runs a graph on a fake carrying both scripts, and folds the
// boilerplate the call tests share.
func runCallGraph(t *testing.T, y string, cfg Config, calls map[string][]CallResult, callErr map[string]error) (Result, *FakeRuntime) {
	t.Helper()
	rt := NewFakeRuntime(time.Unix(0, 0))
	rt.Script = okScript("steps.plan")
	rt.CallScript = calls
	rt.CallErr = callErr
	g := graphFromYAML(t, y)
	r, err := New(g, rt, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, xerr := r.Execute(t.Context())
	if xerr != nil {
		t.Fatalf("Execute: %v (events:\n%s)", xerr, strings.Join(r.Events(), "\n"))
	}
	return res, rt
}

// TestCallStepRunsSubworkflow pins the whole call step in one document: the
// node renders its params against run state, asks the door once, records the
// child's bindings as its value, and a downstream template reads one binding
// through the scope.
func TestCallStepRunsSubworkflow(t *testing.T) {
	calls := map[string][]CallResult{
		"helper": {{
			Succeeded:  true,
			ChildRunID: "caller!steps.sub",
			Status:     RunSucceeded,
			Outputs:    map[string]string{"summary": "done"},
		}},
	}
	res, fake := runCallGraph(t, callYAML, Config{RunID: "caller"}, calls, nil)

	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason %q (events:\n%s)", res.Status, res.Reason, strings.Join(res.Events, "\n"))
	}
	st := nodeOf(t, res, "steps.sub")
	if st.State != StateSucceeded {
		t.Fatalf("call node state = %s (%s)", st.State, st.Reason)
	}
	if st.CallRunID != "caller!steps.sub" {
		t.Errorf("CallRunID = %q, want the child run the door named", st.CallRunID)
	}
	// The rendered params reached the door: the delegate's report text
	// interpolated into the call's `task` param.
	asked := fake.Calls()
	if len(asked) != 1 {
		t.Fatalf("the door was asked %d times, want 1", len(asked))
	}
	if got := asked[0].Params["task"]; got != "for done steps.plan" {
		t.Errorf("params[task] = %v, want the rendered delegate report", got)
	}
	if asked[0].Workflow != "helper" {
		t.Errorf("request workflow = %q, want helper", asked[0].Workflow)
	}
	if asked[0].MaxDepth != 0 {
		t.Errorf("request max_depth = %d, want undeclared", asked[0].MaxDepth)
	}
	if asked[0].ParentRunID != "caller" || asked[0].NodePath != "steps.sub" {
		t.Errorf("request identity = %s / %s, want the parent run and node path", asked[0].ParentRunID, asked[0].NodePath)
	}
	// The emit read the child's binding through the call node's value — the
	// decode-on-demand walk a structured report's fields get.
	out := nodeOf(t, res, "steps.report")
	if out.Result != "child said: done" {
		t.Errorf("emit rendered %q, want the child's summary", out.Result)
	}
	// The call itself cost nothing: the dispatch tally is the delegate's.
	if res.Dispatches != 1 {
		t.Errorf("dispatches = %d, want 1 (a call prices nothing itself)", res.Dispatches)
	}
}

// TestCallStepChildFailed pins the failure fold: a child run that did not
// succeed fails the node with a reason naming the subworkflow and its run
// record, and the default abort policy stops the run before the emit.
func TestCallStepChildFailed(t *testing.T) {
	calls := map[string][]CallResult{
		"helper": {{
			Succeeded:  false,
			ChildRunID: "caller!steps.sub",
			Status:     RunFailed,
			Reason:     "steps.w1: dispatch_failed: the provider exploded",
		}},
	}
	res, _ := runCallGraph(t, callYAML, Config{RunID: "caller"}, calls, nil)
	// The default disposition of a failed node is abort: the run stops with
	// the node's reason on it.
	if res.Status != RunAborted {
		t.Fatalf("status = %s (%s), want aborted", res.Status, res.Reason)
	}
	st := nodeOf(t, res, "steps.sub")
	if st.State != StateFailed {
		t.Fatalf("call node state = %s", st.State)
	}
	for _, want := range []string{ReasonCallFailed, "helper", "caller!steps.sub", "provider exploded"} {
		if !strings.Contains(st.Reason, want) {
			t.Errorf("reason %q does not name %q", st.Reason, want)
		}
	}
}

// TestCallStepChildFailedContinues pins that on_failure: continue is the same
// declaration for a call it is for a delegate: the run proceeds past the
// failed child, whose outputs a downstream step must not pretend to read.
func TestCallStepChildFailedContinues(t *testing.T) {
	y := strings.Replace(callYAML, "needs: [plan]\n      params:", "needs: [plan]\n      on_failure: continue\n      params:", 1)
	y = strings.Replace(y, `value: "child said: {{ steps.sub.output.summary }}"`, `value: "ran past the failed child"`, 1)
	calls := map[string][]CallResult{
		"helper": {{Succeeded: false, ChildRunID: "caller!steps.sub", Status: RunFailed, Reason: "boom"}},
	}
	res, _ := runCallGraph(t, y, Config{RunID: "caller"}, calls, nil)
	// The run continues past the node and closes failed: the exposed failure
	// is the run's verdict, which is what on_failure: continue means here —
	// downstream work happened anyway.
	if res.Status != RunFailed {
		t.Fatalf("status = %s (%s), want the run closed failed on the exposed failure", res.Status, res.Reason)
	}
	st := nodeOf(t, res, "steps.sub")
	if st.State != StateFailed {
		t.Fatalf("call node state = %s, want failed", st.Reason)
	}
	if st.Outputs != nil {
		t.Errorf("failed call recorded outputs: %+v", st.Outputs)
	}
	if out := nodeOf(t, res, "steps.report"); out.Result != "ran past the failed child" {
		t.Errorf("emit rendered %q", out.Result)
	}
}

// TestCallStepDoorMalfunction pins the door-error path: a Call that errored
// is a runtime error on the node, and the door was asked once with no outcome
// to place blame on.
func TestCallStepDoorMalfunction(t *testing.T) {
	callErr := map[string]error{"helper": errors.New("the registry was reloaded mid-run")}
	res, fake := runCallGraph(t, callYAML, Config{RunID: "caller"}, nil, callErr)
	if res.Status != RunAborted {
		t.Fatalf("status = %s, want aborted", res.Status)
	}
	st := nodeOf(t, res, "steps.sub")
	if !strings.Contains(st.Reason, ReasonRuntimeError) {
		t.Errorf("reason %q does not name the runtime error", st.Reason)
	}
	if !strings.Contains(st.Reason, "Runtime.Call") {
		t.Errorf("reason %q does not name the door method", st.Reason)
	}
	if len(fake.Calls()) != 1 {
		t.Errorf("door asks = %d, want 1 (the error is the answer, not a retry)", len(fake.Calls()))
	}
}

// TestCallStepParamRenderFailure pins that a param that compiles but will
// not render against run state fails the node before the door is asked. A
// reference an author gets wrong statically is a WF010 finding Resolve
// rejects before this package sees the graph; the run-state case is a field
// the delivered value does not carry.
func TestCallStepParamRenderFailure(t *testing.T) {
	y := strings.Replace(callYAML, "{{ steps.plan.output }}", "{{ steps.plan.output.nope }}", 1)
	res, fake := runCallGraph(t, y, Config{RunID: "caller"}, nil, nil)
	if res.Status != RunAborted {
		t.Fatalf("status = %s, want aborted", res.Status)
	}
	st := nodeOf(t, res, "steps.sub")
	if !strings.Contains(st.Reason, ReasonCallFailed) {
		t.Errorf("reason %q does not name call_failed", st.Reason)
	}
	if len(fake.Calls()) != 0 {
		t.Error("the door was asked despite the param render failure")
	}
}

// TestCallStepSucceededWithoutOutputs pins the empty success: a child that
// produced no bindings still succeeds, and the node's recorded value is the
// empty map the door reported.
func TestCallStepSucceededWithoutOutputs(t *testing.T) {
	calls := map[string][]CallResult{
		"helper": {{Succeeded: true, ChildRunID: "caller!steps.sub", Status: RunSucceeded}},
	}
	res, _ := runCallGraph(t, callYAML, Config{RunID: "caller"}, calls, nil)
	st := nodeOf(t, res, "steps.sub")
	if st.State != StateSucceeded {
		t.Fatalf("call node state = %s (%s)", st.State, st.Reason)
	}
	if st.Result != "{}" {
		t.Errorf("recorded value = %q, want the empty binding map", st.Result)
	}
}
