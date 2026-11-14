package exec

// loop_branch_aggregate_test.go pins loop, branch, and aggregate execution
// (yaah-0u0v.15). Everything runs against the FakeRuntime; nothing sleeps.

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoopStopsAtMaxIterations(t *testing.T) {
	y := strings.Replace(specHead, "%s", "loop-bound", 1) + `  steps:
    - id: l
      kind: loop
      max_iterations: 3
      collect: outcomes
      body:
        kind: delegate
        role: developer
        prompt: "do"
`
	g := graphFromYAML(t, y)
	script := okScript(iterationPath("steps.l", 0, "steps.l.body[0]"), iterationPath("steps.l", 1, "steps.l.body[0]"), iterationPath("steps.l", 2, "steps.l.body[0]"))
	res, _ := runGraph(t, g, script, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, eventLog(res))
	}
	l := nodeOf(t, res, "steps.l")
	if len(l.Iterations) != 3 {
		t.Errorf("iterations = %d, want 3", len(l.Iterations))
	}
	if len(l.Collect) != 3 {
		t.Errorf("collect = %d, want 3", len(l.Collect))
	}
}

func TestLoopStopsAtUntil(t *testing.T) {
	y := strings.Replace(specHead, "%s", "loop-until", 1) + `  steps:
    - id: l
      kind: loop
      max_iterations: 5
      until: { from: steps.l.collect, exists: true }
      collect: outcomes
      body:
        kind: delegate
        role: developer
        prompt: "do"
`
	g := graphFromYAML(t, y)
	// Two iterations scripted; the until (collect exists) must stop the loop
	// after the first, so the second is never started.
	script := okScript(iterationPath("steps.l", 0, "steps.l.body[0]"), iterationPath("steps.l", 1, "steps.l.body[0]"))
	res, _ := runGraph(t, g, script, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, eventLog(res))
	}
	l := nodeOf(t, res, "steps.l")
	if len(l.Iterations) != 1 {
		t.Errorf("iterations = %d, want 1 (until matched after the first)", len(l.Iterations))
	}
}

func TestLoopUnboundedRefused(t *testing.T) {
	y := strings.Replace(specHead, "%s", "loop-unbounded", 1) + `  steps:
    - id: l
      kind: loop
      collect: outcomes
      body:
        kind: delegate
        role: developer
        prompt: "do"
`
	g := graphFromYAML(t, y)
	res, _ := runGraph(t, g, nil, Config{})
	l := nodeOf(t, res, "steps.l")
	if l.State != StateFailed || !strings.Contains(l.Reason, ReasonLoopUnbounded) {
		t.Errorf("state = %s reason = %q, want failed with %s", l.State, l.Reason, ReasonLoopUnbounded)
	}
}

func TestBranchRoutesAndSkips(t *testing.T) {
	y := strings.Replace(specHead, "%s", "branch", 1) + `  params:
    verdict:
      type: string
  steps:
    - id: b
      kind: branch
      on: { from: params.verdict }
      cases:
        - value: pass
          then: good
        - value: fail
          then: bad
      default: good
    - id: good
      kind: delegate
      role: developer
      prompt: "good"
    - id: bad
      kind: delegate
      role: developer
      prompt: "bad"
`
	g := graphFromYAML(t, y)
	script := okScript("steps.good")
	res, _ := runGraph(t, g, script, Config{Params: map[string]any{"verdict": "pass"}})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, eventLog(res))
	}
	b := nodeOf(t, res, "steps.b")
	if b.Selected != "steps.good" {
		t.Errorf("selected = %q, want steps.good", b.Selected)
	}
	bad := nodeOf(t, res, "steps.bad")
	if bad.State != StateSkipped || !strings.Contains(bad.Reason, ReasonBranchArmNotSelected) {
		t.Errorf("bad = %s %q, want skipped with %s", bad.State, bad.Reason, ReasonBranchArmNotSelected)
	}
}

func TestBranchDefaultFallthrough(t *testing.T) {
	y := strings.Replace(specHead, "%s", "branch-default", 1) + `  params:
    verdict:
      type: string
  steps:
    - id: b
      kind: branch
      on: { from: params.verdict }
      cases:
        - value: pass
          then: good
      default: fallback
    - id: good
      kind: delegate
      role: developer
      prompt: "good"
    - id: fallback
      kind: delegate
      role: developer
      prompt: "fallback"
`
	g := graphFromYAML(t, y)
	script := okScript("steps.fallback")
	res, _ := runGraph(t, g, script, Config{Params: map[string]any{"verdict": "unknown"}})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, eventLog(res))
	}
	b := nodeOf(t, res, "steps.b")
	if b.Selected != "steps.fallback" {
		t.Errorf("selected = %q, want steps.fallback", b.Selected)
	}
	if got := nodeOf(t, res, "steps.good").State; got != StateSkipped {
		t.Errorf("good = %s, want skipped", got)
	}
}

func TestAggregateConcat(t *testing.T) {
	y := strings.Replace(specHead, "%s", "agg-concat", 1) + `  params:
    list1:
      type: list
    list2:
      type: list
  steps:
    - id: agg
      kind: aggregate
      strategy: concat
      inputs:
        - { from: params.list1 }
        - { from: params.list2 }
`
	g := graphFromYAML(t, y)
	res, _ := runGraph(t, g, nil, Config{Params: map[string]any{"list1": []any{1.0, 2.0}, "list2": []any{3.0}}})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, eventLog(res))
	}
	a := nodeOf(t, res, "steps.agg")
	if want := []any{1.0, 2.0, 3.0}; !reflect.DeepEqual(a.Combined, want) {
		t.Errorf("combined = %#v, want %#v", a.Combined, want)
	}
}

func TestAggregateJSONMerge(t *testing.T) {
	y := strings.Replace(specHead, "%s", "agg-merge", 1) + `  params:
    obj1:
      type: map
    obj2:
      type: map
  steps:
    - id: agg
      kind: aggregate
      strategy: json_merge
      inputs:
        - { from: params.obj1 }
        - { from: params.obj2 }
`
	g := graphFromYAML(t, y)
	res, _ := runGraph(t, g, nil, Config{Params: map[string]any{
		"obj1": map[string]any{"a": "1", "b": "2"},
		"obj2": map[string]any{"b": "3", "c": "4"},
	}})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, eventLog(res))
	}
	a := nodeOf(t, res, "steps.agg")
	if want := map[string]any{"a": "1", "b": "3", "c": "4"}; !reflect.DeepEqual(a.Combined, want) {
		t.Errorf("combined = %#v, want %#v (later key wins)", a.Combined, want)
	}
}

func TestAggregateSummarizeDispatchesBody(t *testing.T) {
	y := strings.Replace(specHead, "%s", "agg-summarize", 1) + `  params:
    topic:
      type: string
  steps:
    - id: agg
      kind: aggregate
      strategy: summarize
      inputs:
        - { from: params.topic }
      body:
        kind: delegate
        role: developer
        prompt: "summarize"
`
	g := graphFromYAML(t, y)
	// A summarize's body keeps its own template path (exactly one execution).
	script := map[string][]Completion{"steps.agg.body[0]": {Report("the summary", 0)}}
	res, fake := runGraph(t, g, script, Config{Params: map[string]any{"topic": "t"}})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, eventLog(res))
	}
	if got := len(fake.Started()); got != 1 {
		t.Errorf("dispatched %d times, want exactly 1 summarize dispatch", got)
	}
	a := nodeOf(t, res, "steps.agg")
	if a.Combined != "the summary" {
		t.Errorf("combined = %#v, want the body's report", a.Combined)
	}
}

func TestGateStillRefused(t *testing.T) {
	y := strings.Replace(specHead, "%s", "gate", 1) + `  steps:
    - id: g
      kind: gate
      assert:
        name: nonempty
`
	g := graphFromYAML(t, y)
	res, fake := runGraph(t, g, nil, Config{})
	n := nodeOf(t, res, "steps.g")
	// A gate is no longer a not_implemented kind (yaah-0u0v.21): this one has no
	// needs and no `from`, so its subject is the empty need list and `nonempty`
	// fails it. With no on_fail the gate ends failed, which is the escalation path
	// the run's own on_failure disposition (abort by default) then raises.
	if n.State != StateFailed || !strings.Contains(n.Reason, ReasonGateFailed) {
		t.Errorf("gate = %s %q, want failed with %s", n.State, n.Reason, ReasonGateFailed)
	}
	if strings.Contains(n.Reason, ReasonNotImplemented) {
		t.Errorf("gate reason = %q, still claims not_implemented", n.Reason)
	}
	if len(fake.Started()) != 0 {
		t.Errorf("a gate under assert dispatched %d attempt(s)", len(fake.Started()))
	}
}
