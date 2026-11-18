package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/registry"
)

// call_test.go is the composed end-to-end half of yaah-0u0v.23: a parent run
// whose call node resolves through a real registry on disk, a child run
// composed by the door from the same runner and store, and the idempotency
// and depth contracts the door alone can enforce.

const helperSpec = `apiVersion: yaah.workflow/v1
kind: Workflow
metadata: {name: helper}
spec:
  params:
    task: {type: string, required: true}
  steps:
    - {id: work, kind: delegate, role: developer, prompt: "do {{ params.task }}"}
    - {id: summary, kind: emit, needs: [work], value: "done: {{ steps.work.output }}"}
`

const callerSpec = `apiVersion: yaah.workflow/v1
kind: Workflow
metadata: {name: caller}
spec:
  steps:
    - {id: plan, kind: delegate, role: developer, prompt: plan it}
    - id: sub
      kind: call
      workflow: helper
      needs: [plan]
      params: {task: "the {{ steps.plan.output }}"}
    - {id: report, kind: emit, needs: [sub], value: "child said {{ steps.sub.output.summary }}"}
`

// promptRunner is the sub-agent seam: it records every prompt it was handed
// and answers from a script, in call order — the same discipline the recorder
// in host_test.go follows, with the prompt kept for the assertions here.
// The slice is mutex-guarded because the door runs dispatches in their own
// goroutines: two nodes dispatched in the same cycle reach this closure
// concurrently.
type promptRunner struct {
	mu      sync.Mutex
	prompts []string
	results []string
}

func (r *promptRunner) run(_ context.Context, prompt string, _ jobs.SubAgentParams) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := len(r.prompts)
	r.prompts = append(r.prompts, prompt)
	if i < len(r.results) {
		return r.results[i], nil
	}
	return "the report", nil
}

// promptAt is one recorded prompt under the same lock the runner writes
// with: an assertion after a killed run may race a dispatch goroutine that
// has not wound down yet.
func (r *promptRunner) promptAt(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prompts[i]
}

// promptCount is the recorded count under the same lock.
func (r *promptRunner) promptCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prompts)
}

// memStore is the exec.Store the compose tests hold: a map of checkpoints,
// no serialization — the same process writes and reads.
type memStore struct {
	mu          sync.Mutex
	checkpoints map[string]exec.Checkpoint
}

func newMemStore() *memStore { return &memStore{checkpoints: map[string]exec.Checkpoint{}} }

func (s *memStore) SaveCheckpoint(_ context.Context, cp exec.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints[cp.RunID] = cp
	return nil
}

func (s *memStore) LoadCheckpoint(_ context.Context, runID string) (exec.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, ok := s.checkpoints[runID]
	if !ok {
		return exec.Checkpoint{}, fmt.Errorf("%w: %s", exec.ErrRunNotFound, runID)
	}
	return cp, nil
}

func (s *memStore) Close() error { return nil }

// callRegistry writes helperSpec under a temp root and opens the registry.
func callRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "helper.yaml"), []byte(helperSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatalf("registry.Open: %v", err)
	}
	return reg
}

// TestCallComposesNestedRun pins the composed contract end to end: the parent
// delegates, its call node resolves through the registry, the door composes a
// child run from the same runner, the child's rendered prompt carries the
// params the parent rendered, and the parent's emit reads the child's emit
// binding by name.
func TestCallComposesNestedRun(t *testing.T) {
	reg := callRegistry(t)
	runner := &promptRunner{results: []string{"the workstream", "child work"}}
	res, err := Execute(context.Background(), Options{
		Graph:  graphFromYAML(t, callerSpec),
		Runner: runner.run,
		Calls:  reg,
	})
	if err != nil {
		t.Fatalf("Execute: %v\nlog:\n%s", err, logOf(res.Run))
	}
	if res.Run.Status != exec.RunSucceeded {
		t.Fatalf("status = %s (%s)\nlog:\n%s", res.Run.Status, res.Run.Reason, logOf(res.Run))
	}
	st := nodeState(t, res.Run, "steps.sub")
	if st.State != exec.StateSucceeded {
		t.Fatalf("call node state = %s (%s)", st.State, st.Reason)
	}
	if st.CallRunID != "workflow:caller!steps.sub" {
		t.Errorf("child run id = %q, want the derived record name", st.CallRunID)
	}
	// The child's delegate got the params the parent rendered into the call.
	if runner.promptCount() != 2 {
		t.Fatalf("dispatches = %d, want the parent's plan and the child's work", runner.promptCount())
	}
	if !strings.Contains(runner.promptAt(1), "do the the workstream") {
		t.Errorf("child prompt = %q, want the rendered param", runner.promptAt(1))
	}
	// The parent read the child's binding through the call node's value.
	out := nodeState(t, res.Run, "steps.report")
	if out.Result != "child said done: child work" {
		t.Errorf("emit rendered %q, want the child's summary", out.Result)
	}
}

// TestCallReAskReadsFinishedChildFromRecord pins the re-ask idempotency: a
// second Call for the same parent run and node finds the child's finished
// record and projects its outcome, re-executing nothing.
func TestCallReAskReadsFinishedChildFromRecord(t *testing.T) {
	reg := callRegistry(t)
	runner := &promptRunner{}
	st := newMemStore()
	rt := NewRuntime(runner.run)
	rt.calls = reg
	rt.runStore = st

	req := exec.CallRequest{
		Workflow:    "helper",
		MaxDepth:    0,
		Params:      map[string]any{"task": "the thing"},
		ParentRunID: "root-run",
		NodePath:    "steps.sub",
	}
	first, err := rt.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("first Call: %v", err)
	}
	if !first.Succeeded || first.Outputs["summary"] == "" {
		t.Fatalf("first Call result = %+v, want the child's summary", first)
	}
	dispatches := runner.promptCount()

	second, err := rt.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("second Call: %v", err)
	}
	if !second.Succeeded {
		t.Fatalf("second Call result = %+v, want the record's disposition", second)
	}
	if second.Outputs["summary"] != first.Outputs["summary"] {
		t.Errorf("second Call outputs = %v, want the recorded %v", second.Outputs, first.Outputs)
	}
	if runner.promptCount() != dispatches {
		t.Errorf("runner dispatches grew from %d to %d: a finished child was re-executed", dispatches, runner.promptCount())
	}
}

// TestCallDepthGuardRefusesBeyondBound pins the runtime recursion bound: a
// self-recursive workflow under max_depth enters the copies its bound admits
// and the door refuses the next — a refusal that names the frame and the
// declaration, whatever a drifted registry said between validate and run.
func TestCallDepthGuardRefusesBeyondBound(t *testing.T) {
	root := t.TempDir()
	self := `apiVersion: yaah.workflow/v1
kind: Workflow
metadata: {name: selfhelper}
spec:
  steps:
    - {id: w, kind: delegate, role: developer, prompt: work}
    - {id: rec, kind: call, workflow: selfhelper, max_depth: 2}
`
	if err := os.WriteFile(filepath.Join(root, "selfhelper.yaml"), []byte(self), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatalf("registry.Open: %v", err)
	}
	runner := &promptRunner{}
	rt := NewRuntime(runner.run)
	rt.calls = reg
	res, rerr := rt.Call(context.Background(), exec.CallRequest{
		Workflow:    "selfhelper",
		MaxDepth:    2,
		ParentRunID: "root-run",
		NodePath:    "steps.sub",
	})
	if rerr != nil {
		t.Fatalf("Call: %v", rerr)
	}
	if res.Succeeded {
		t.Fatal("the self-recursive workflow succeeded; the depth guard was expected to refuse it")
	}
	// The refusal names the frame the guard held and the declaration that
	// bound it.
	if !strings.Contains(res.Reason, "frame 4") || !strings.Contains(res.Reason, "max_depth is 2") {
		t.Errorf("reason %q does not state the guarded frame and the declaration", res.Reason)
	}
	// max_depth: 2 admits three frames — the entered copy and two more — and
	// the run-record derivation names the copies the run reached before the
	// guard refused: the deepest nested call's record is three edges down.
	if !strings.Contains(res.Reason, "root-run!steps.sub!steps.rec") {
		t.Errorf("reason %q does not carry the nested run records", res.Reason)
	}
}

// TestCallResumesChildFromMidFlightRecord pins the .16 crash-safety contract
// for calls: a child run whose process died with an attempt in flight — its
// checkpoint mid-flight, no terminal disposition — is CONTINUED by the re-ask
// a resumed parent makes: the abandoned attempt is re-dispatched, the
// finished record's dispatch tally carries both attempts, and nothing is
// executed from scratch.
func TestCallResumesChildFromMidFlightRecord(t *testing.T) {
	reg := callRegistry(t)
	entry, err := reg.Load("helper")
	if err != nil {
		t.Fatalf("registry load: %v", err)
	}
	runner := &promptRunner{}
	st := newMemStore()
	childID := exec.ChildRunID("root-run", "steps.sub")

	// Craft the crash: drive the child run through the exported exec seam
	// with a fake that holds its one attempt in flight, persisting the
	// dispatch_started event, then abandon the Run mid-cycle. The fake holds
	// no goroutine, so abandoning leaks nothing — the record is exactly
	// what a killed process would have written at that instant.
	fake := exec.NewFakeRuntime(time.Unix(0, 0))
	fake.Hold = map[string]bool{"steps.work": true}
	childCfg := exec.Config{
		RunID:    childID,
		Params:   map[string]any{"task": "the thing"},
		SpecHash: entry.SpecHash,
		SpecPath: entry.Path,
		Store:    st,
	}
	crashed, cerr := exec.New(entry.Graph, fake, childCfg)
	if cerr != nil {
		t.Fatalf("child New: %v", cerr)
	}
	if progressed, aerr := crashed.Advance(context.Background()); !progressed || aerr != nil {
		t.Fatalf("run_started cycle: progressed=%v err=%v", progressed, aerr)
	}
	if progressed, aerr := crashed.Advance(context.Background()); !progressed || aerr != nil {
		t.Fatalf("dispatch cycle: progressed=%v err=%v", progressed, aerr)
	}
	// The record now holds one in-flight attempt and no disposition.
	cp, err := st.LoadCheckpoint(context.Background(), childID)
	if err != nil {
		t.Fatalf("child checkpoint: %v", err)
	}
	if cp.Status != exec.RunRunning {
		t.Fatalf("child status = %s, want running (the crash left a mid-flight record)", cp.Status)
	}

	// The re-ask: the door resumes the child from its record instead of
	// executing a fresh child.
	rt := NewRuntime(runner.run)
	rt.calls = reg
	rt.runStore = st
	res, rerr := rt.Call(context.Background(), exec.CallRequest{
		Workflow:    "helper",
		Params:      map[string]any{"task": "the thing"},
		ParentRunID: "root-run",
		NodePath:    "steps.sub",
		MaxDepth:    0,
	})
	if rerr != nil {
		t.Fatalf("re-ask Call: %v", rerr)
	}
	if !res.Succeeded {
		t.Fatalf("the resumed child did not succeed: %+v", res)
	}
	// Exactly one real dispatch: the re-dispatched attempt. The abandoned
	// one was the fake's, and resume re-dispatches rather than re-running
	// the child from scratch.
	if runner.promptCount() != 1 {
		t.Errorf("runner dispatches = %d, want the one resumed attempt\nprompts: %v", len(runner.prompts), runner.prompts)
	}
	// The finished record carries BOTH attempts: the interrupted one the
	// crash left and the resumed one — the witness that the run continued
	// rather than started over.
	final, err := st.LoadCheckpoint(context.Background(), childID)
	if err != nil {
		t.Fatalf("final checkpoint: %v", err)
	}
	if final.Status != exec.RunSucceeded {
		t.Errorf("final status = %s, want succeeded", final.Status)
	}
	if final.Dispatches != 2 {
		t.Errorf("final dispatches = %d, want 2 (the interrupted attempt and the resumed one)", final.Dispatches)
	}
}
