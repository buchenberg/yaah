package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/dispatch"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// The tests below drive a real workflow run through the real scheduler and the
// real internal/dispatch core. The seam is dispatch.Run's jobs.TaskRunner: a
// scripted closure answers the lane, so every test here makes no model call,
// opens no provider connection, and never sleeps — the acceptance criterion for
// the sequential golden is exactly "runs end to end against the deterministic
// runner".

const specHead = "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata:\n  name: %s\nspec:\n"

func graphFromYAML(t *testing.T, y string) *ir.Graph {
	t.Helper()
	doc, err := spec.ParseYAML([]byte(y))
	if err != nil {
		t.Fatalf("parsing spec: %v\nspec was:\n%s", err, y)
	}
	g, err := ir.Resolve(doc)
	if err != nil {
		t.Fatalf("resolving graph: %v", err)
	}
	return g
}

func goldenGraph(t *testing.T, name string) *ir.Graph {
	t.Helper()
	data, err := os.ReadFile("../spec/testdata/valid/" + name + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	return graphFromYAML(t, string(data))
}

// recorder is the deterministic sub-agent runner: it answers from a script and
// records every request it received.
type recorder struct {
	results []string
	errs    []error
	calls   int
}

func (r *recorder) run(_ context.Context, _ string, _ jobs.SubAgentParams) (string, error) {
	i := r.calls
	r.calls++
	if i < len(r.errs) && r.errs[i] != nil {
		return "", r.errs[i]
	}
	// Scripted results are indexed by the call number minus the leading
	// error slots they follow: errs consume the first len(errs) calls.
	if j := i - len(r.errs); j >= 0 && j < len(r.results) {
		return r.results[j], nil
	}
	return "the report", nil
}

func nodeState(t *testing.T, res exec.Result, path string) *exec.NodeState {
	t.Helper()
	for _, n := range res.Nodes {
		if n.Path == path {
			return n
		}
	}
	t.Fatalf("node %s missing from result (have %d)", path, len(res.Nodes))
	return nil
}

func outputOf(t *testing.T, res exec.Result, name string) exec.NodeOutput {
	t.Helper()
	for _, o := range res.Outputs {
		if o.Name == name {
			return o
		}
	}
	t.Fatalf("output %q missing from run outputs (have %v)", name, res.Outputs)
	return exec.NodeOutput{}
}

func logOf(res exec.Result) string { return strings.Join(res.Events, "\n") }

// TestGoldenSequentialEndToEnd runs the sequential golden — one report-lane
// delegate feeding one emit — end to end, and pins the whole .12 contract at
// once: the delegate dispatches through internal/dispatch with its rendered
// prompt and declared role, the emit settles without a dispatch, both outputs
// are recorded on the run, and both appear in the event log.
func TestGoldenSequentialEndToEnd(t *testing.T) {
	g := goldenGraph(t, "sequential")
	plan := `{"workstreams":[{"id":"door"}]}`
	runner := &recorder{results: []string{plan}}

	res, err := Execute(context.Background(), Options{
		Graph:  g,
		Params: map[string]any{"task": "ship the door"},
		Runner: runner.run,
	})
	if err != nil {
		t.Fatalf("Execute: %v\nlog:\n%s", err, logOf(res.Run))
	}
	if res.Run.Status != exec.RunSucceeded {
		t.Fatalf("status = %s reason=%q, want succeeded\nlog:\n%s", res.Run.Status, res.Run.Reason, logOf(res.Run))
	}
	if runner.calls != 1 {
		t.Errorf("model dispatches = %d, want 1 (emit must not dispatch)", runner.calls)
	}
	if res.Run.Dispatches != 1 {
		t.Errorf("run dispatches = %d, want 1", res.Run.Dispatches)
	}

	reqs := res.Requests()
	if len(reqs) != 1 {
		t.Fatalf("door requests = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Lane != dispatch.LaneReport {
		t.Errorf("lane = %q, want %q", got.Lane, dispatch.LaneReport)
	}
	if got.Params.Role != "planner" {
		t.Errorf("role = %q, want planner", got.Params.Role)
	}
	if !strings.Contains(got.Prompt, `Break "ship the door" into independent workstreams.`) {
		t.Errorf("prompt was not rendered from the compiled segments:\n%s", got.Prompt)
	}
	if strings.Contains(got.Prompt, "{{") {
		t.Errorf("rendered prompt still carries a template marker:\n%s", got.Prompt)
	}

	if st := nodeState(t, res.Run, "steps.plan"); st.State != exec.StateSucceeded || st.Result != plan {
		t.Errorf("delegate node state = %s result=%q, want succeeded with the report text", st.State, st.Result)
	}
	// The delegate's declared output name binds the delivered report text;
	// the emit's value is bound under its step id because it declared no
	// `output:` name.
	if o := outputOf(t, res.Run, "Plan"); o.Value != plan || o.Kind != ir.KindDelegate {
		t.Errorf("delegate output = %+v, want the report text", o)
	}
	o := outputOf(t, res.Run, "announce")
	if o.Kind != ir.KindEmit || !strings.Contains(o.Value, "Planning complete for ship the door.") {
		t.Errorf("emit output = %+v, want the rendered value", o)
	}
	for _, want := range []string{"dispatch_started steps.plan#1", "attempt_completed steps.plan#1", "node_succeeded steps.plan outputs=Plan", "node_succeeded steps.announce outputs=announce"} {
		if !strings.Contains(logOf(res.Run), want) {
			t.Errorf("event log missing %q\nlog:\n%s", want, logOf(res.Run))
		}
	}
	if len(res.Artifacts) != 0 {
		t.Errorf("artifacts = %v, want none (the golden declares no path)", res.Artifacts)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("door warnings = %v, want none", res.Warnings)
	}
}

// TestDelegateOutputFeedsNextDelegate pins the report lane's value flow: the
// scope snapshot the door renders against carries a succeeded delegate's
// recorded result, so `steps.<id>.output` resolves for the next node.
func TestDelegateOutputFeedsNextDelegate(t *testing.T) {
	y := strings.Replace(specHead, "%s", "chain", 1) + `  steps:
    - id: first
      kind: delegate
      role: developer
      prompt: do the first thing
    - id: second
      kind: delegate
      needs: [first]
      role: reviewer
      prompt: review {{ steps.first.output }}
`
	runner := &recorder{results: []string{"FIRST RESULT", "second done"}}
	res, err := Execute(context.Background(), Options{Graph: graphFromYAML(t, y), Runner: runner.run})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	reqs := res.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if reqs[1].Prompt != "review FIRST RESULT" {
		t.Errorf("second prompt = %q, want the first node's report rendered in", reqs[1].Prompt)
	}
}

// TestTypedOutputSchemaSetsJSONMode pins how a declared output schema rides the
// response-contract machinery: schema ⇒ json_mode ⇒ provider response_format,
// with the verdict mirrored from the envelope and never recomputed here.
func TestTypedOutputSchemaSetsJSONMode(t *testing.T) {
	y := strings.Replace(specHead, "%s", "typed", 1) + `  steps:
    - id: check
      kind: delegate
      role: tester
      lane: report
      timeout_seconds: 7
      max_turns: 4
      provider: p1
      model: m1
      output:
        name: verdict
        type: Verdict
        schema: verdict.schema.json
      prompt: check the build
`
	runner := &recorder{results: []string{"{\"verdict\":\"pass\"}"}}
	res, err := Execute(context.Background(), Options{Graph: graphFromYAML(t, y), Runner: runner.run})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := res.Requests()[0]
	if !got.Params.JSONMode {
		t.Error("declared output schema did not set json_mode on the dispatch")
	}
	if got.Params.Provider != "p1" || got.Params.Model != "m1" || got.Params.MaxToolTurns != 4 {
		t.Errorf("per-node overrides not passed: %+v", got.Params)
	}
	if got.Timeout != 7*time.Second || got.Params.TimeoutSeconds != 7 {
		t.Errorf("timeout = %s params=%d, want 7s on both", got.Timeout, got.Params.TimeoutSeconds)
	}
	// The lane asserted no contract, so the recorded verdict is empty — which
	// is "unchecked", not "passed".
	if o := outputOf(t, res.Run, "verdict"); o.Contract != "" {
		t.Errorf("contract = %q, want empty while the field is reserved", o.Contract)
	}
}

// TestRetryIsObservableAndBounded drives a scripted retryable failure through
// the real classifier: DispatchTimedOut ⇒ stop_reason=timeout ⇒
// recovery.retryable=true, which is the only retry brain the run consults.
// The backoff is honored through the door's Wait, so no test sleeps.
func TestRetryIsObservableAndBounded(t *testing.T) {
	y := strings.Replace(specHead, "%s", "retry", 1) + `  steps:
    - id: flaky
      kind: delegate
      role: developer
      retry:
        max_attempts: 3
        backoff_seconds: 0.5
      prompt: do the flaky thing
`
	runner := &recorder{
		errs:    []error{context.DeadlineExceeded, context.DeadlineExceeded},
		results: []string{"third time"},
	}
	var waited []time.Duration
	res, err := Execute(context.Background(), Options{
		Graph:  graphFromYAML(t, y),
		Runner: runner.run,
		Wait: func(context.Context, time.Duration) error {
			waited = append(waited, 500*time.Millisecond)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Run.Status != exec.RunSucceeded {
		t.Fatalf("status = %s reason=%q, want succeeded", res.Run.Status, res.Run.Reason)
	}
	st := nodeState(t, res.Run, "steps.flaky")
	if len(st.Attempts) != 3 || runner.calls != 3 {
		t.Fatalf("attempts = %d calls = %d, want 3 of each", len(st.Attempts), runner.calls)
	}
	if a := st.LastAttempt(); a.Result != "third time" || a.StopReason != "completed" {
		t.Errorf("last attempt = %+v, want the third result", a)
	}
	if a := st.Attempts[0]; a.StopReason != "timeout" || !a.Retryable || !a.RecoveryAsserted {
		t.Errorf("first attempt = %+v, want stop_reason=timeout and the envelope's retryable assertion", a)
	}
	if got := strings.Count(logOf(res.Run), string(exec.EventRetryScheduled)); got != 2 {
		t.Errorf("retry_scheduled events = %d, want 2\nlog:\n%s", got, logOf(res.Run))
	}
	if !strings.Contains(logOf(res.Run), "stop_reason=timeout") {
		t.Errorf("timeout is not observable in the event log:\n%s", logOf(res.Run))
	}
	if len(waited) != 2 {
		t.Errorf("backoffs = %v, want two 500ms waits through the door", waited)
	}
}

// TestRetryCeilingStopsTheRun pins the bound: the author's max_attempts is the
// ceiling, the reason names it, and the run stops rather than looping.
func TestRetryCeilingStopsTheRun(t *testing.T) {
	y := strings.Replace(specHead, "%s", "ceiling", 1) + `  steps:
    - id: doomed
      kind: delegate
      role: developer
      retry:
        max_attempts: 2
      prompt: always fails
`
	runner := &recorder{errs: []error{errors.New("provider exploded"), errors.New("provider exploded")}}
	res, err := Execute(context.Background(), Options{Graph: graphFromYAML(t, y), Runner: runner.run})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 2 {
		t.Errorf("calls = %d, want the declared ceiling of 2", runner.calls)
	}
	if res.Run.Status != exec.RunAborted {
		t.Errorf("status = %s, want aborted", res.Run.Status)
	}
	st := nodeState(t, res.Run, "steps.doomed")
	if !strings.Contains(st.Reason, "attempt ceiling 2 reached") {
		t.Errorf("reason = %q, want the ceiling named", st.Reason)
	}
}

// TestProposalLaneIsDeclinedAtTheDoor pins that a node this lane cannot honor
// is refused with the owning bead named, and that nothing was dispatched.
// TestProposalLaneIsDeclinedAtTheDoor pins the honest refusal that survives
// yaah-0u0v.13: the proposal lane is live, but only when the door holds the
// session's retained runtime — without it, sealing would leak a proposal
// nobody can settle, so the node fails at the door before any work starts.
func TestProposalLaneIsDeclinedAtTheDoor(t *testing.T) {
	y := strings.Replace(specHead, "%s", "proposed", 1) + `  steps:
    - id: work
      kind: delegate
      role: developer
      lane: proposal
      prompt: write the change
`
	runner := &recorder{}
	res, err := Execute(context.Background(), Options{Graph: graphFromYAML(t, y), Runner: runner.run})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 0 {
		t.Errorf("calls = %d, want 0 — a declined node must not reach the runner", runner.calls)
	}
	st := nodeState(t, res.Run, "steps.work")
	if st.State != exec.StateFailed || !strings.Contains(st.Reason, "retained proposal runtime") {
		t.Errorf("node = %s reason=%q, want a failure naming the missing retained runtime", st.State, st.Reason)
	}
}

// TestEmitArtifactWrittenInsideRoot pins the artifact door: only a declared
// path is written, and it lands under the artifact root.
func TestEmitArtifactWrittenInsideRoot(t *testing.T) {
	y := strings.Replace(specHead, "%s", "artifact", 1) + `  params:
    task:
      type: string
  steps:
    - id: note
      kind: emit
      value: result for {{ params.task }}
      path: out/note.md
`
	root := t.TempDir()
	res, err := Execute(context.Background(), Options{
		Graph:        graphFromYAML(t, y),
		Params:       map[string]any{"task": "x"},
		Runner:       (&recorder{}).run,
		ArtifactRoot: root,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("artifacts = %v, want one written", res.Artifacts)
	}
	data, err := os.ReadFile(filepath.Join(root, "out", "note.md"))
	if err != nil {
		t.Fatalf("reading the written artifact: %v", err)
	}
	if string(data) != "result for x" {
		t.Errorf("artifact content = %q, want the rendered emit value", data)
	}
	if res.Artifacts[0].Declared != "out/note.md" || res.Artifacts[0].NodePath != "steps.note" {
		t.Errorf("artifact record = %+v, want the producing node and declared path", res.Artifacts[0])
	}
}

// TestEmitArtifactOutsideRootRefused pins the restriction: a declared path that
// resolves outside the artifact root is a refusal, not a write.
func TestEmitArtifactOutsideRootRefused(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "..", "escape.md")
	before, _ := filepath.Glob(outside)
	y := strings.Replace(specHead, "%s", "escape", 1) + `  steps:
    - id: sneaky
      kind: emit
      value: nope
      path: ../escape.md
`
	res, err := Execute(context.Background(), Options{
		Graph:        graphFromYAML(t, y),
		Runner:       (&recorder{}).run,
		ArtifactRoot: root,
	})
	if err == nil {
		t.Fatal("an escaping artifact path must be refused")
	}
	if !strings.Contains(err.Error(), "escapes the artifact root") {
		t.Errorf("err = %v, want the escape named", err)
	}
	if len(res.Artifacts) != 0 {
		t.Errorf("artifacts = %v, want none written", res.Artifacts)
	}
	if len(before) != 0 {
		t.Errorf("%s was created outside the root", outside)
	}
	if res.Run.Status != exec.RunSucceeded {
		t.Errorf("run status = %s, want the node itself to have succeeded", res.Run.Status)
	}
}

// TestEmitArtifactWithoutRootDeclines pins that a declared artifact with no
// configured root is reported, never silently skipped.
func TestEmitArtifactWithoutRootDeclines(t *testing.T) {
	y := strings.Replace(specHead, "%s", "noroot", 1) + `  steps:
    - id: note
      kind: emit
      value: text
      path: out/note.md
`
	res, err := Execute(context.Background(), Options{Graph: graphFromYAML(t, y), Runner: (&recorder{}).run})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Run.Status != exec.RunSucceeded {
		t.Fatalf("status = %s, want succeeded", res.Run.Status)
	}
	if len(res.Artifacts) != 0 {
		t.Errorf("artifacts = %v, want none", res.Artifacts)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "no artifact root") {
		t.Errorf("warnings = %v, want the declined artifact named", res.Warnings)
	}
}

// TestUnenforceableIslandsWarned pins that a node which ran without the
// isolation it declared is visible in the run's warnings.
func TestUnenforceableIslandsWarned(t *testing.T) {
	y := strings.Replace(specHead, "%s", "island", 1) + `  steps:
    - id: work
      kind: delegate
      role: developer
      world: worktree
      grants:
        - path: src
          access: readwrite
      prompt: do it
`
	res, err := Execute(context.Background(), Options{Graph: graphFromYAML(t, y), Runner: (&recorder{results: []string{"ok"}}).run})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Run.Status != exec.RunSucceeded {
		t.Fatalf("status = %s, want succeeded", res.Run.Status)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "could not be enforce") || !strings.Contains(joined, "world: worktree") {
		t.Errorf("warnings = %q, want the unenforced world and grants named", joined)
	}
}

// TestExecuteRejectsMissingInput pins the constructor guards.
func TestExecuteRejectsMissingInput(t *testing.T) {
	if _, err := Execute(context.Background(), Options{Runner: (&recorder{}).run}); err == nil {
		t.Error("nil graph must be refused")
	}
	g := graphFromYAML(t, strings.Replace(specHead, "%s", "nilrun", 1)+"  steps:\n    - id: a\n      kind: emit\n      value: x\n")
	if _, err := Execute(context.Background(), Options{Graph: g}); err == nil {
		t.Error("a nil runner must be refused")
	}
	if _, err := NewRuntime(nil).Start(context.Background(), exec.Dispatch{}); err == nil {
		t.Error("a dispatch with no node must be refused")
	}
}

// TestDeadlineCancelDoesNotRecurse is the regression pin for the composite
// cancel closure. A dispatch under a wall-clock budget gets a WithDeadline
// layer whose cancel closes over both layers; a closure that captured the
// rebound variable instead of the original cancel recursed into itself
// forever. The recursion fired in the door's goroutine AFTER the run had
// already succeeded, racing process exit — so this test calls the stored
// cancel synchronously, where the overflow cannot hide.
func TestDeadlineCancelDoesNotRecurse(t *testing.T) {
	g := goldenGraph(t, "sequential") // declares budgets.max_wall_clock_seconds
	runner := &recorder{results: []string{"plan"}}
	res, err := Execute(context.Background(), Options{
		Graph:  g,
		Params: map[string]any{"task": "verify"},
		Runner: runner.run,
	})
	if err != nil {
		t.Fatalf("Execute: %v\nlog:\n%s", err, logOf(res.Run))
	}
	if res.Run.Status != exec.RunSucceeded {
		t.Fatalf("status = %s, want succeeded\nlog:\n%s", res.Run.Status, logOf(res.Run))
	}
	// The plan attempt ran under a deadline, so its stored cancel is the
	// composite. Calling it must terminate.
	if err := res.Runtime.Cancel(exec.Handle{DispatchID: "steps.plan#1"}); err != nil {
		t.Logf("cancel of a completed attempt reported: %v (acceptable; the pin is that it returns at all)", err)
	}
}

// TestGateJudgeReadsJudgePromptAndRole pins the judge dispatch through the real
// door: a gate under `judge` hands the container node to the door, so the door
// must read the prompt and role from the judge payload, not the container's
// empty delegate fields (yaah-0u0v.21).
func TestGateJudgeReadsJudgePromptAndRole(t *testing.T) {
	y := strings.Replace(specHead, "%s", "gate", 1) + `  steps:
    - id: g
      kind: gate
      judge: {role: judgebot, prompt: "is the build green?"}
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
	runner := &recorder{results: []string{`{"pass": true}`}}
	res, err := Execute(context.Background(), Options{Graph: graphFromYAML(t, y), Runner: runner.run})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	reqs := res.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want exactly the judge dispatch", len(reqs))
	}
	if reqs[0].Prompt != "is the build green?" {
		t.Errorf("judge prompt = %q, want the judge payload's prompt", reqs[0].Prompt)
	}
	if reqs[0].Params.Role != "judgebot" {
		t.Errorf("judge role = %q, want the judge payload's role", reqs[0].Params.Role)
	}
	if got := nodeState(t, res.Run, "steps.g"); got.State != exec.StateSucceeded || got.Disposition != "pass" {
		t.Errorf("gate = %s %q, want succeeded with pass disposition", got.State, got.Reason)
	}
}
