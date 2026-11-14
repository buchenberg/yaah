package exec

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// Deterministic scheduler tests (bead yaah-0u0v.11). Every test drives the
// real reducer through the real driver against FakeRuntime: scripted
// outcomes, virtual clock, no sleeping. The acceptance criteria pinned here:
// ordering, concurrency caps, retries, failure/skip, cancellation, budgets,
// and the not_implemented scope fence for non-delegate kinds.

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
	doc, err := spec.ParseYAML(data)
	if err != nil {
		t.Fatalf("parsing golden %s: %v", name, err)
	}
	g, err := ir.Resolve(doc)
	if err != nil {
		t.Fatalf("resolving golden %s: %v", name, err)
	}
	return g
}

func nodeOf(t *testing.T, res Result, path string) *NodeState {
	t.Helper()
	for _, n := range res.Nodes {
		if n.Path == path {
			return n
		}
	}
	t.Fatalf("node %s missing from result (have %d nodes)", path, len(res.Nodes))
	return nil
}

func startedPaths(fake *FakeRuntime) []string {
	ds := fake.Started()
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Node.Path)
	}
	return out
}

func eventLog(res Result) string { return strings.Join(res.Events, "\n") }

// runGraph executes g with the given script and config on a fresh fake at
// virtual epoch, failing the test on transport errors.
func runGraph(t *testing.T, g *ir.Graph, script map[string][]Completion, cfg Config) (Result, *FakeRuntime) {
	t.Helper()
	rt := NewFakeRuntime(time.Unix(0, 0))
	rt.Script = script
	r, err := New(g, rt, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v (events so far:\n%s)", err, strings.Join(r.Events(), "\n"))
	}
	return res, rt
}

func okScript(paths ...string) map[string][]Completion {
	m := make(map[string][]Completion, len(paths))
	for _, p := range paths {
		m[p] = []Completion{Report("done "+p, 0)}
	}
	return m
}

const chainYAML = specHead + `  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: do a
    - id: b
      kind: delegate
      needs: [a]
      role: developer
      prompt: do b
    - id: c
      kind: delegate
      needs: [b]
      role: developer
      prompt: do c
`

// TestChainOrderingAndDeterminism pins topological execution order and the
// determinism contract: same graph + same scripted outcomes ⇒ identical event
// sequence across runs.
func TestChainOrderingAndDeterminism(t *testing.T) {
	y := strings.Replace(chainYAML, "%s", "chain", 1)
	g := graphFromYAML(t, y)
	script := okScript("steps.a", "steps.b", "steps.c")

	res1, fake1 := runGraph(t, g, script, Config{RunID: "run-1"})
	if res1.Status != RunSucceeded {
		t.Fatalf("status = %s, want succeeded (reason %q)", res1.Status, res1.Reason)
	}
	for _, p := range []string{"steps.a", "steps.b", "steps.c"} {
		if n := nodeOf(t, res1, p); n.State != StateSucceeded {
			t.Errorf("%s state = %s, want succeeded", p, n.State)
		}
	}
	got := strings.Join(startedPaths(fake1), ",")
	if got != "steps.a,steps.b,steps.c" {
		t.Errorf("dispatch order = %s, want steps.a,steps.b,steps.c", got)
	}
	if res1.Dispatches != 3 {
		t.Errorf("dispatches = %d, want 3", res1.Dispatches)
	}

	res2, _ := runGraph(t, g, script, Config{RunID: "run-1"})
	if eventLog(res1) != eventLog(res2) {
		t.Error("event log differs between two identical runs — determinism broken")
	}
}

// TestReadyQueueCanonicalOrder pins that independent ready nodes start in
// canonical (path-sorted) order regardless of author order.
func TestReadyQueueCanonicalOrder(t *testing.T) {
	y := strings.Replace(specHead, "%s", "diamond", 1) + `  steps:
    - id: root
      kind: delegate
      role: developer
      prompt: r
    - id: zeta
      kind: delegate
      needs: [root]
      role: developer
      prompt: z
    - id: alpha
      kind: delegate
      needs: [root]
      role: developer
      prompt: a
    - id: join
      kind: delegate
      needs: [zeta, alpha]
      role: developer
      prompt: j
`
	g := graphFromYAML(t, y)
	res, fake := runGraph(t, g, okScript("steps.root", "steps.zeta", "steps.alpha", "steps.join"), Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s (%q)", res.Status, res.Reason)
	}
	got := strings.Join(startedPaths(fake), ",")
	want := "steps.root,steps.alpha,steps.zeta,steps.join"
	if got != want {
		t.Errorf("canonical start order = %s, want %s", got, want)
	}
}

// TestConcurrencyCapNeverExceeded pins the hard invariant: the scheduler
// never has more attempts open than MaxConcurrentDispatches, at several caps,
// with more ready nodes than any cap.
func TestConcurrencyCapNeverExceeded(t *testing.T) {
	y := strings.Replace(specHead, "%s", "wide", 1) + `  steps:
    - {id: n1, kind: delegate, role: developer, prompt: "1"}
    - {id: n2, kind: delegate, role: developer, prompt: "2"}
    - {id: n3, kind: delegate, role: developer, prompt: "3"}
    - {id: n4, kind: delegate, role: developer, prompt: "4"}
    - {id: n5, kind: delegate, role: developer, prompt: "5"}
    - {id: n6, kind: delegate, role: developer, prompt: "6"}
`
	g := graphFromYAML(t, y)
	script := okScript("steps.n1", "steps.n2", "steps.n3", "steps.n4", "steps.n5", "steps.n6")
	for _, capN := range []int{1, 2, 3} {
		res, fake := runGraph(t, g, script, Config{Limits: Limits{MaxConcurrentDispatches: capN}})
		if res.Status != RunSucceeded {
			t.Fatalf("cap %d: status = %s (%q)", capN, res.Status, res.Reason)
		}
		if fake.Peak() > capN {
			t.Errorf("cap %d: runtime saw %d concurrent attempts", capN, fake.Peak())
		}
		if res.PeakInFlight > capN {
			t.Errorf("cap %d: reducer peak in-flight = %d", capN, res.PeakInFlight)
		}
		if res.Dispatches != 6 {
			t.Errorf("cap %d: dispatches = %d, want 6", capN, res.Dispatches)
		}
	}
}

// TestRetryRetryableThenSucceeds pins envelope-driven retry: a retryable
// failure schedules another attempt after the declared backoff, and the node
// succeeds on the second attempt.
func TestRetryRetryableThenSucceeds(t *testing.T) {
	y := strings.Replace(specHead, "%s", "retry", 1) + `  steps:
    - id: flaky
      kind: delegate
      role: developer
      prompt: p
      retry:
        max_attempts: 3
        backoff_seconds: 7
    - id: after
      kind: delegate
      needs: [flaky]
      role: developer
      prompt: q
`
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		"steps.flaky": {Failure("timeout", "transient", 0), Report("ok", 0)},
		"steps.after": {Report("done", 0)},
	}
	res, fake := runGraph(t, g, script, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s (%q)", res.Status, res.Reason)
	}
	n := nodeOf(t, res, "steps.flaky")
	if n.State != StateSucceeded {
		t.Errorf("flaky state = %s, want succeeded", n.State)
	}
	if len(n.Attempts) != 2 {
		t.Errorf("attempts = %d, want 2 (one retryable failure + one success)", len(n.Attempts))
	}
	if !strings.Contains(eventLog(res), string(EventRetryScheduled)) {
		t.Error("event log has no retry_scheduled event")
	}
	backoffs := fake.Backoffs()
	if len(backoffs) != 1 || backoffs[0] != 7*time.Second {
		t.Errorf("backoffs = %v, want [7s] (declared backoff honored via Runtime.Backoff)", backoffs)
	}
	if got := strings.Join(startedPaths(fake), ","); got != "steps.flaky,steps.flaky,steps.after" {
		t.Errorf("start order = %s, want flaky twice then after", got)
	}
}

// TestRetryCeilingExhaustedAborts pins: a retryable failure that exhausts
// max_attempts fails the node, the reason names the ceiling, and the default
// abort policy stops the run — downstream nodes never dispatch.
func TestRetryCeilingExhaustedAborts(t *testing.T) {
	y := strings.Replace(specHead, "%s", "exhausted", 1) + `  steps:
    - id: doomed
      kind: delegate
      role: developer
      prompt: p
      retry:
        max_attempts: 2
        backoff_seconds: 0
    - id: after
      kind: delegate
      needs: [doomed]
      role: developer
      prompt: q
`
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		"steps.doomed": {Failure("timeout", "t1", 0), Failure("timeout", "t2", 0)},
		"steps.after":  {Report("never", 0)},
	}
	res, fake := runGraph(t, g, script, Config{})
	if res.Status != RunAborted {
		t.Errorf("status = %s, want aborted (default on_failure is abort)", res.Status)
	}
	n := nodeOf(t, res, "steps.doomed")
	if n.State != StateFailed {
		t.Errorf("doomed state = %s, want failed", n.State)
	}
	if !strings.Contains(n.Reason, "attempt ceiling 2 reached") {
		t.Errorf("reason = %q, want it to name the attempt ceiling", n.Reason)
	}
	if len(n.Attempts) != 2 {
		t.Errorf("attempts = %d, want exactly the ceiling 2", len(n.Attempts))
	}
	if strings.Contains(strings.Join(startedPaths(fake), ","), "steps.after") {
		t.Error("downstream node dispatched despite abort")
	}
}

// TestTerminalFailureDoesNotRetry pins: a non-retryable envelope
// (recovery.retryable false) fails the node on the first attempt — retries
// are earned by the envelope, not by the attempt budget.
func TestTerminalFailureDoesNotRetry(t *testing.T) {
	y := strings.Replace(specHead, "%s", "terminal", 1) + `  steps:
    - id: doomed
      kind: delegate
      role: developer
      prompt: p
      retry:
        max_attempts: 3
        backoff_seconds: 0
`
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		"steps.doomed": {TerminalFailure("provider_error", "model unavailable", 0)},
	}
	res, fake := runGraph(t, g, script, Config{})
	n := nodeOf(t, res, "steps.doomed")
	if n.State != StateFailed {
		t.Fatalf("state = %s, want failed", n.State)
	}
	if len(n.Attempts) != 1 {
		t.Errorf("attempts = %d, want 1 (terminal failure must not retry)", len(n.Attempts))
	}
	if !strings.Contains(n.Reason, "provider_error") {
		t.Errorf("reason = %q, want it to name the stop_reason", n.Reason)
	}
	if strings.Contains(eventLog(res), string(EventRetryScheduled)) {
		t.Error("retry_scheduled emitted for a terminal failure")
	}
	if len(fake.Started()) != 1 {
		t.Errorf("starts = %d, want 1", len(fake.Started()))
	}
}

// TestOnFailureContinueExposesDownstream pins the bead criterion: a failed
// node with on_failure continue does not stop the run — the failure is
// exposed downstream and the run proceeds.
func TestOnFailureContinueExposesDownstream(t *testing.T) {
	y := strings.Replace(specHead, "%s", "continue-past", 1) + `  steps:
    - id: flaky
      kind: delegate
      role: developer
      prompt: p
      on_failure: continue
    - id: after
      kind: delegate
      needs: [flaky]
      role: developer
      prompt: q
`
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		"steps.flaky": {TerminalFailure("halted", "stopped", 0)},
		"steps.after": {Report("done", 0)},
	}
	res, _ := runGraph(t, g, script, Config{})
	if res.Status == RunAborted {
		t.Fatalf("run aborted despite on_failure: continue (reason %q)", res.Reason)
	}
	if n := nodeOf(t, res, "steps.flaky"); n.State != StateFailed {
		t.Errorf("flaky state = %s, want failed", n.State)
	}
	if n := nodeOf(t, res, "steps.after"); n.State != StateSucceeded {
		t.Errorf("after state = %s, want succeeded (failure exposed downstream, run continues)", n.State)
	}
	if !strings.Contains(res.Reason, "steps.flaky") {
		t.Errorf("result reason = %q, want it to name the exposed failure", res.Reason)
	}
}

// TestOnFailureSkipPropagates pins: on_failure skip ends the node skipped and
// skip propagates — a node whose need was skipped skips too, naming the need.
func TestOnFailureSkipPropagates(t *testing.T) {
	y := strings.Replace(specHead, "%s", "skip-chain", 1) + `  steps:
    - id: optional
      kind: delegate
      role: developer
      prompt: p
      on_failure: skip
    - id: depends
      kind: delegate
      needs: [optional]
      role: developer
      prompt: q
    - id: tail
      kind: delegate
      needs: [depends]
      role: developer
      prompt: r
`
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		"steps.optional": {TerminalFailure("halted", "stopped", 0)},
		"steps.depends":  {Report("never", 0)},
		"steps.tail":     {Report("never", 0)},
	}
	res, fake := runGraph(t, g, script, Config{})
	opt := nodeOf(t, res, "steps.optional")
	if opt.State != StateSkipped && opt.State != StateFailed {
		t.Errorf("optional state = %s, want skipped (or failed with skip policy)", opt.State)
	}
	dep := nodeOf(t, res, "steps.depends")
	if dep.State != StateSkipped {
		t.Errorf("depends state = %s, want skipped", dep.State)
	}
	if !strings.Contains(dep.Reason, ReasonUpstreamSkipped) || !strings.Contains(dep.Reason, "steps.optional") {
		t.Errorf("depends reason = %q, want upstream_skipped naming steps.optional", dep.Reason)
	}
	if tail := nodeOf(t, res, "steps.tail"); tail.State != StateSkipped {
		t.Errorf("tail state = %s, want skipped (propagation is transitive)", tail.State)
	}
	started := strings.Join(startedPaths(fake), ",")
	if strings.Contains(started, "steps.depends") || strings.Contains(started, "steps.tail") {
		t.Errorf("skipped nodes reached the door: %s", started)
	}
}

// TestCancelStopsRun pins cooperative cancellation: Cancel before further
// progress ends the run cancelled, and no node is left dangling in a
// non-terminal state.
func TestCancelStopsRun(t *testing.T) {
	y := strings.Replace(chainYAML, "%s", "cancel", 1)
	g := graphFromYAML(t, y)
	rt := NewFakeRuntime(time.Unix(0, 0))
	rt.Script = okScript("steps.a", "steps.b", "steps.c")
	r, err := New(g, rt, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r.Cancel("user abort")
	res, err := r.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute after cancel: %v", err)
	}
	if res.Status != RunCancelled {
		t.Fatalf("status = %s, want cancelled (reason %q)", res.Status, res.Reason)
	}
	for _, n := range res.Nodes {
		if n.State == StateRunning || n.State == StateReady {
			t.Errorf("node %s left in non-terminal state %s after cancel", n.Path, n.State)
		}
	}
}

// TestCancelDuringRunCancelsInFlight pins mid-run cancellation: cancel
// between Advance cycles leaves the run cancelled.
func TestCancelDuringRunCancelsInFlight(t *testing.T) {
	y := strings.Replace(chainYAML, "%s", "cancel-mid", 1)
	g := graphFromYAML(t, y)
	rt := NewFakeRuntime(time.Unix(0, 0))
	rt.Script = okScript("steps.a", "steps.b", "steps.c")
	r, err := New(g, rt, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := r.Advance(ctx); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	r.Cancel("mid-run abort")
	res, err := r.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != RunCancelled {
		t.Errorf("status = %s, want cancelled", res.Status)
	}
}

// TestDispatchBudgetExceeded pins the dispatch-count budget: the run stops
// deterministically at the cap instead of overrunning it.
func TestDispatchBudgetExceeded(t *testing.T) {
	y := strings.Replace(chainYAML, "%s", "budget", 1)
	g := graphFromYAML(t, y)
	res, fake := runGraph(t, g, okScript("steps.a", "steps.b", "steps.c"),
		Config{Limits: Limits{MaxDispatches: 2}})
	if res.Status != RunBudgetExceeded {
		t.Fatalf("status = %s, want budget_exceeded (reason %q)", res.Status, res.Reason)
	}
	if res.Dispatches > 2 {
		t.Errorf("dispatches = %d, want <= 2", res.Dispatches)
	}
	if strings.Contains(strings.Join(startedPaths(fake), ","), "steps.c") {
		t.Error("node c dispatched past the budget")
	}
	if !strings.Contains(eventLog(res), string(EventBudgetExceeded)) {
		t.Error("event log has no budget_exceeded event")
	}
}

// TestWallClockBudgetExceeded pins the wall-clock budget against the virtual
// clock: scripted durations push elapsed time past MaxWallClock and the run
// stops before dispatching further nodes.
func TestWallClockBudgetExceeded(t *testing.T) {
	y := strings.Replace(chainYAML, "%s", "wallclock", 1)
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		"steps.a": {Report("slow a", 3*time.Second)},
		"steps.b": {Report("slow b", 3*time.Second)},
		"steps.c": {Report("never", 0)},
	}
	res, fake := runGraph(t, g, script, Config{Limits: Limits{MaxWallClock: 5 * time.Second}})
	if res.Status != RunBudgetExceeded {
		t.Fatalf("status = %s, want budget_exceeded (reason %q)", res.Status, res.Reason)
	}
	if strings.Contains(strings.Join(startedPaths(fake), ","), "steps.c") {
		t.Error("node c dispatched past the wall-clock budget")
	}
	if !strings.Contains(eventLog(res), string(EventBudgetExceeded)) {
		t.Error("event log has no budget_exceeded event")
	}
}

// TestNonDelegateKindsRefused pins the scope fence: container, settle, and
// selector kinds end not_implemented instead of faking success that would
// contradict validate's proof of the same graph. The refusal reason names
// the owning bead or door where one is assigned.
func TestNonDelegateKindsRefused(t *testing.T) {
	params := map[string]any{"task": "verify"}
	for _, golden := range []string{"fanout_settle", "bounded_repair", "speculative"} {
		t.Run(golden, func(t *testing.T) {
			g := goldenGraph(t, golden)
			res, _ := runGraph(t, g, nil, Config{Params: params})
			if res.Status == RunSucceeded {
				t.Fatal("run with unimplemented kinds reported success — contradicts the validator's proof")
			}
			found := false
			for _, n := range res.Nodes {
				if strings.Contains(n.Reason, ReasonNotImplemented) {
					found = true
				}
			}
			if !found {
				t.Errorf("no node ended not_implemented (status %s, reason %q)", res.Status, res.Reason)
			}
			// Delegates that ran before the refusal must not be marked failed
			// by association — the refusal is the refusing node's own.
			for _, n := range res.Nodes {
				if n.Kind == ir.KindDelegate && n.State == StateFailed &&
					strings.Contains(n.Reason, ReasonNotImplemented) {
					t.Errorf("delegate node %s refused as not_implemented — only delegate dispatches in this bead", n.Path)
				}
			}
		})
	}
}

// TestNewRejectsBadInput pins constructor validation.
func TestNewRejectsBadInput(t *testing.T) {
	rt := NewFakeRuntime(time.Unix(0, 0))
	if _, err := New(nil, rt, Config{}); err == nil {
		t.Error("New(nil graph) should fail")
	}
	y := strings.Replace(chainYAML, "%s", "ctor", 1)
	g := graphFromYAML(t, y)
	if _, err := New(g, nil, Config{}); err == nil {
		t.Error("New(nil runtime) should fail")
	}
}
