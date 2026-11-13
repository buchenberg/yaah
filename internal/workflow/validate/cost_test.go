package validate

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// costGraph resolves a steps block for the cost family, the same
// discipline as authorityGraph: these rules describe a graph, so a
// document that never became one is a broken test rather than a finding.
func costGraph(t *testing.T, steps string) *ir.Graph {
	t.Helper()
	g, diags := Validate(documentWith(steps))
	if g == nil {
		t.Fatalf("document did not resolve, cannot exercise the cost rules:%s", render(diags))
	}
	return g
}

// oneDispatch is the cheapest document that costs anything: one delegate.
// A budgets block is spelled out in front of it at each use, so no case
// here depends on where a prefix happens to end.
const (
	oneDispatch     = "  steps:\n" + oneDispatchItem
	oneDispatchItem = "    - {id: a, kind: delegate, role: developer, prompt: hi}\n"
)

// selectorFanout adds a fan-out whose item list only the run knows, behind
// a two-dispatch budget: the shape that makes the worst case a floor.
const selectorFanout = "  budgets:\n    max_dispatches: 2\n  steps:\n" +
	"    - {id: a, kind: delegate, role: developer, prompt: hi}\n" +
	"    - id: f\n      kind: fanout\n      over: {from: steps.a.output.workstreams}\n" +
	"      max_parallel: 2\n      body: {kind: delegate, role: developer, prompt: hi}\n"

// ------------------------------------------------------------------- WF033

func TestDispatchBudget(t *testing.T) {
	cases := []struct {
		name  string
		steps string
		env   Env
		path  string // "" for silent
		msg   string
	}{
		{
			name: "under the declared budget",
			steps: "  budgets:\n    max_dispatches: 12\n  steps:\n" +
				"    - {id: a, kind: delegate, role: developer, prompt: hi}\n" +
				"    - id: l\n      kind: loop\n      max_iterations: 3\n      body:\n" +
				"        - {kind: delegate, role: developer, prompt: fix}\n" +
				"        - {kind: delegate, role: tester, prompt: check}\n",
		},
		{
			name: "cost bomb: loop over fan-out",
			steps: "  budgets:\n    max_dispatches: 8\n  steps:\n    - id: l\n      kind: loop\n      max_iterations: 50\n      body:\n" +
				"        - kind: fanout\n          over: [a, b, c, d, e, f, g, h, i, j]\n          max_parallel: 10\n" +
				"          body: {kind: delegate, role: developer, prompt: hi}\n",
			path: "spec.budgets.max_dispatches",
			msg:  "at most 500 dispatches, over the 8 declared",
		},
		{
			name:  "selector fan-out is priced as a floor",
			steps: selectorFanout,
			path:  "spec.budgets.max_dispatches",
			msg:   "at least 3 dispatches",
		},
		{
			name:  "a host item ceiling turns the floor into a ceiling",
			steps: selectorFanout, env: Env{MaxFanoutItems: 10},
			path: "spec.budgets.max_dispatches",
			msg:  "at most 11 dispatches",
		},
		{
			name: "a floor under budget proves nothing",
			steps: "  budgets:\n    max_dispatches: 64\n  steps:\n" +
				"    - {id: a, kind: delegate, role: developer, prompt: hi}\n" +
				"    - id: f\n      kind: fanout\n      over: {from: steps.a.output.workstreams}\n" +
				"      max_parallel: 2\n      body: {kind: delegate, role: developer, prompt: hi}\n",
		},
		{
			name:  "a declared budget may not widen the host ceiling",
			steps: "  budgets:\n    max_dispatches: 100\n" + "  steps:\n" + oneDispatchItem,
			env:   Env{MaxDispatches: 32},
			path:  "spec.budgets.max_dispatches",
			msg:   "widens the host ceiling 32",
		},
		{
			name: "the host ceiling applies with no declared budget",
			steps: "  steps:\n    - id: f\n      kind: fanout\n      over: [a, b, c, d]\n      max_parallel: 4\n" +
				"      body: {kind: delegate, role: developer, prompt: hi}\n",
			env:  Env{MaxDispatches: 2},
			path: "spec.budgets",
			msg:  "over the host ceiling 2; declare budgets.max_dispatches",
		},
		{
			name: "no ceiling anywhere is unchecked",
			steps: "  steps:\n    - id: l\n      kind: loop\n      max_iterations: 20\n" +
				"      body: {kind: delegate, role: developer, prompt: hi}\n",
		},
		{
			name:  "a zero budget allows no dispatch",
			steps: "  budgets:\n    max_dispatches: 0\n" + "  steps:\n" + oneDispatchItem,
			path:  "spec.budgets.max_dispatches",
			msg:   "at most 1 dispatches, over the 0 declared",
		},
		{
			name:  "a negative budget is refused once",
			steps: "  budgets:\n    max_dispatches: -1\n" + "  steps:\n" + oneDispatchItem,
			path:  "spec.budgets.max_dispatches",
			msg:   "must not be negative",
		},
		{
			name:  "retries add dispatches",
			steps: "  budgets:\n    max_dispatches: 2\n  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, retry: {max_attempts: 3}}\n",
			path:  "spec.budgets.max_dispatches",
			msg:   "at most 3 dispatches",
		},
		{
			name: "a judge is a dispatch",
			steps: "  budgets:\n    max_dispatches: 1\n  steps:\n" +
				"    - {id: a, kind: delegate, role: developer, prompt: hi}\n" +
				"    - {id: s, kind: select, judge: {role: reviewer, prompt: pick}}\n",
			path: "spec.budgets.max_dispatches",
			msg:  "at most 2 dispatches",
		},
		{
			name: "a nested loop saturates instead of wrapping",
			steps: "  budgets:\n    max_dispatches: 12\n  steps:\n    - id: outer\n      kind: loop\n      max_iterations: 1000000000\n      body:\n" +
				"        - kind: loop\n          max_iterations: 1000000000\n          body: {kind: delegate, role: developer, prompt: hi}\n",
			path: "spec.budgets.max_dispatches",
			msg:  fmt.Sprintf("at most %d dispatches", costSaturate),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := Cost(costGraph(t, tc.steps), tc.env)
			if tc.path == "" {
				requireSilent(t, diags, CodeWF033)
				return
			}
			requireCode(t, diags, CodeWF033, tc.path)
			if d := findingAt(t, diags, CodeWF033, tc.path); !strings.Contains(d.Message, tc.msg) {
				t.Errorf("message %q does not contain %q", d.Message, tc.msg)
			}
		})
	}
}

// TestUnreachableWorkIsNotPriced keeps the cost model on the same join-all
// reachability as WF004: a loop stranded behind a need that names no step
// spends nothing, and 100 iterations of it must not read as a budget break.
func TestUnreachableWorkIsNotPriced(t *testing.T) {
	g := &ir.Graph{Budgets: &ir.Budgets{MaxDispatches: costIntPtr(12)}, Steps: []*ir.Node{
		{Path: "steps.a", Kind: ir.KindDelegate, Role: "developer"},
		{
			Path: "steps.l", Kind: ir.KindLoop, MaxIterations: costIntPtr(100),
			Needs: []string{"nowhere"},
			Body:  []*ir.Node{{Path: "steps.l.body[0]", Kind: ir.KindDelegate, Role: "developer"}},
		},
	}}
	est := estimateCost(g, Env{})
	if est.Dispatches != 1 {
		t.Errorf("Dispatches = %d, want 1: the stranded loop must not be counted", est.Dispatches)
	}
	if !est.Complete {
		t.Errorf("Complete = false, want true; Unbound = %v", est.Unbound)
	}
	if hasCode(Cost(g, Env{}), CodeWF033) {
		t.Error("WF033 fired on unreachable work")
	}
}

// ------------------------------------------------------------------- WF034

// TestRepetitionBounds covers the residue only: max_iterations and
// max_parallel are optional at both decode and resolve — neither the tag
// set nor the resolver refuses an absent bound — so a loop with no bound
// reaches this rule instead of failing as WF001. costGraph proves that by
// getting a graph at all in every case below.
func TestRepetitionBounds(t *testing.T) {
	cases := []struct {
		name  string
		steps string
		path  string
		msg   string
	}{
		{
			name:  "loop without max_iterations",
			steps: "  steps:\n    - id: l\n      kind: loop\n      body: {kind: delegate, role: developer, prompt: hi}\n",
			path:  "steps.l.max_iterations",
			msg:   "no bound to price",
		},
		{
			name:  "loop with a zero bound",
			steps: "  steps:\n    - {id: l, kind: loop, max_iterations: 0, body: {kind: delegate, role: developer, prompt: hi}}\n",
			path:  "steps.l.max_iterations",
			msg:   "must be at least 1",
		},
		{
			name:  "fan-out without max_parallel",
			steps: "  steps:\n    - {id: f, kind: fanout, over: [a, b], body: {kind: delegate, role: developer, prompt: hi}}\n",
			path:  "steps.f.max_parallel",
			msg:   "item concurrency is unbounded",
		},
		{
			name:  "fan-out with a zero bound",
			steps: "  steps:\n    - {id: f, kind: fanout, over: [a, b], max_parallel: 0, body: {kind: delegate, role: developer, prompt: hi}}\n",
			path:  "steps.f.max_parallel",
			msg:   "must be at least 1",
		},
		{
			name: "both bounds declared",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi}\n" +
				"    - {id: f, kind: fanout, over: [a, b], max_parallel: 2, body: {kind: delegate, role: developer, prompt: hi}}\n" +
				"    - {id: l, kind: loop, max_iterations: 2, body: {kind: delegate, role: developer, prompt: hi}}\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := Cost(costGraph(t, tc.steps), Env{})
			if tc.path == "" {
				requireSilent(t, diags, CodeWF034)
				return
			}
			requireCode(t, diags, CodeWF034, tc.path)
			if d := findingAt(t, diags, CodeWF034, tc.path); !strings.Contains(d.Message, tc.msg) {
				t.Errorf("message %q does not contain %q", d.Message, tc.msg)
			}
		})
	}
}

// TestRepetitionBoundsInBodies covers a bound a document states at any
// depth: a container inside a container, addressed by its node path.
func TestRepetitionBoundsInBodies(t *testing.T) {
	g := &ir.Graph{Steps: []*ir.Node{{
		Path: "steps.f", Kind: ir.KindFanout, MaxParallel: costIntPtr(2),
		Over: &ir.Over{Literals: []any{"a"}},
		Body: []*ir.Node{
			{Path: "steps.f.body[0]", Kind: ir.KindLoop, Body: []*ir.Node{
				{Path: "steps.f.body[0].body[0]", Kind: ir.KindDelegate, Role: "developer"},
			}},
		},
	}}}
	requireCode(t, Cost(g, Env{}), CodeWF034, "steps.f.body[0].max_iterations")
}

// ------------------------------------------------------------------- WF035

func TestConfiguredCaps(t *testing.T) {
	long := "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: " + strings.Repeat("x", 200) + "}\n"
	cases := []struct {
		name  string
		steps string
		env   Env
		path  string
		msg   string
	}{
		{
			name:  "prompt over the configured cap",
			steps: long, env: Env{MaxPromptBytes: 64},
			path: "steps.a.prompt",
			msg:  "prompt is 200 bytes, over the configured cap 64",
		},
		{
			name:  "an unconfigured cap falls back to the decode limit",
			steps: long,
		},
		{
			name: "judge prompt over the cap",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi}\n" +
				"    - {id: s, kind: select, judge: {role: reviewer, prompt: " + strings.Repeat("y", 100) + "}}\n",
			env:  Env{MaxPromptBytes: 64},
			path: "steps.s.judge.prompt",
			msg:  "prompt is 100 bytes",
		},
		{
			name:  "max_turns over the host ceiling",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, max_turns: 100}\n",
			env:   Env{MaxTurnsPerDispatch: 40},
			path:  "steps.a.max_turns",
			msg:   "exceeds the configured per-dispatch ceiling 40",
		},
		{
			name:  "max_turns inside the ceiling",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, max_turns: 30}\n",
			env:   Env{MaxTurnsPerDispatch: 40},
		},
		{
			name:  "max_turns unchecked without a ceiling",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, max_turns: 100}\n",
		},
		{
			name:  "max_tokens over the configured cap",
			steps: "  budgets:\n    max_tokens: 5000\n" + "  steps:\n" + oneDispatchItem,
			env:   Env{MaxTokens: 1000},
			path:  "spec.budgets.max_tokens",
			msg:   "widens the configured cap 1000",
		},
		{
			name:  "max_tokens inside the cap",
			steps: "  budgets:\n    max_tokens: 500\n" + "  steps:\n" + oneDispatchItem,
			env:   Env{MaxTokens: 1000},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := Cost(costGraph(t, tc.steps), tc.env)
			if tc.path == "" {
				requireSilent(t, diags, CodeWF035)
				return
			}
			requireCode(t, diags, CodeWF035, tc.path)
			if d := findingAt(t, diags, CodeWF035, tc.path); !strings.Contains(d.Message, tc.msg) {
				t.Errorf("message %q does not contain %q", d.Message, tc.msg)
			}
		})
	}
}

// ------------------------------------------------------------------ formula

func TestCostEstimateFormula(t *testing.T) {
	const (
		selectorFanoutStep = "  steps:\n    - {id: a, kind: delegate, role: planner, prompt: hi, output: Plan}\n" +
			"    - {id: f, kind: fanout, over: {from: steps.a.output.workstreams}, max_parallel: 5, " +
			"body: {kind: delegate, role: developer, prompt: hi}}\n"
	)
	cases := []struct {
		name       string
		steps      string
		env        Env
		dispatches int
		unbound    string
	}{
		{
			name:       "loop times fan-out times retry",
			steps:      "  steps:\n    - id: l\n      kind: loop\n      max_iterations: 3\n      body:\n        - kind: fanout\n          over: [one, two]\n          max_parallel: 2\n          body: {kind: delegate, role: developer, prompt: hi, retry: {max_attempts: 4}}\n",
			dispatches: 24,
		},
		{
			name:       "a literal fan-out counts items, not concurrency",
			steps:      "  steps:\n    - {id: f, kind: fanout, over: [a, b, c], max_parallel: 1, body: {kind: delegate, role: developer, prompt: hi}}\n",
			dispatches: 3,
		},
		{
			name:       "a selector fan-out falls back to max_parallel",
			steps:      selectorFanoutStep,
			dispatches: 6,
			unbound:    "steps.f",
		},
		{
			name:  "a host item ceiling bounds a selector fan-out",
			steps: selectorFanoutStep, env: Env{MaxFanoutItems: 9},
			dispatches: 10,
		},
		{
			name:       "a call expands by declared depth",
			steps:      "  steps:\n    - {id: c, kind: call, workflow: other, max_depth: 4}\n",
			dispatches: 4,
			unbound:    "steps.c",
		},
		{
			name:       "a call with a host per-call ceiling",
			steps:      "  steps:\n    - {id: c, kind: call, workflow: other, max_depth: 4}\n",
			env:        Env{MaxCallDispatches: 5},
			dispatches: 20,
		},
		{
			name:       "an undeclared call depth is one level",
			steps:      "  steps:\n    - {id: c, kind: call, workflow: other}\n",
			env:        Env{MaxCallDispatches: 5},
			dispatches: 5,
		},
		{
			name:       "an unbounded loop prices one pass and says so",
			steps:      "  steps:\n    - id: l\n      kind: loop\n      body: {kind: delegate, role: developer, prompt: hi}\n",
			dispatches: 1,
			unbound:    "steps.l",
		},
		{
			name:       "a container with no dispatch inside costs nothing",
			steps:      "  steps:\n    - {id: l, kind: loop, max_iterations: 8, body: {kind: emit, value: hi}}\n",
			dispatches: 0,
		},
		{
			name:       "a gate without a judge dispatches nothing",
			steps:      "  steps:\n    - {id: g, kind: gate, assert: {name: ok}}\n",
			dispatches: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			est := estimateCost(costGraph(t, tc.steps), tc.env)
			if est.Dispatches != tc.dispatches {
				t.Errorf("Dispatches = %d, want %d", est.Dispatches, tc.dispatches)
			}
			if got := strings.Join(est.Unbound, " "); got != tc.unbound {
				t.Errorf("Unbound = %q, want %q", got, tc.unbound)
			}
			if est.Complete != (tc.unbound == "") {
				t.Errorf("Complete = %v, want %v", est.Complete, tc.unbound == "")
			}
		})
	}
}

func TestCostEstimateTurnsAndWallClock(t *testing.T) {
	g := costGraph(t, "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, timeout_seconds: 5}\n"+
		"    - id: l\n      kind: loop\n      max_iterations: 2\n      body: {kind: delegate, role: tester, prompt: hi}\n")

	// a costs its declared 5 seconds; the loop's two passes of the tester
	// declare no timeout and take the host's 60 each.
	priced := estimateCost(g, Env{MaxTurnsPerDispatch: 10, SecondsPerDispatch: 60})
	if priced.Dispatches != 3 || priced.Turns != 30 || !priced.TurnsKnown {
		t.Errorf("priced = %+v, want 3 dispatches and 30 known turns", priced)
	}
	if priced.WallClock != 125 || !priced.WallClockKnown {
		t.Errorf("WallClock = %d (known %v), want 125 known", priced.WallClock, priced.WallClockKnown)
	}

	unpriced := estimateCost(g, Env{})
	if unpriced.Dispatches != 3 {
		t.Errorf("Dispatches = %d, want 3", unpriced.Dispatches)
	}
	if unpriced.TurnsKnown || unpriced.Turns != 0 {
		t.Errorf("Turns = %d known %v, want 0 unknown: the host prices no turn budget", unpriced.Turns, unpriced.TurnsKnown)
	}
	if unpriced.WallClockKnown || unpriced.WallClock != 0 {
		t.Errorf("WallClock = %d known %v, want 0 unknown: no timeout on every dispatch", unpriced.WallClock, unpriced.WallClockKnown)
	}

	// A graph whose dispatches all declare a timeout prices itself even
	// when the host does not.
	declared := costGraph(t, "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, timeout_seconds: 30}\n")
	if est := estimateCost(declared, Env{}); !est.WallClockKnown || est.WallClock != 30 {
		t.Errorf("WallClock = %d known %v, want 30 known", est.WallClock, est.WallClockKnown)
	}
}

// ----------------------------------------------------------------- explain

func TestExplainReportsBothHalvesInAggregateOrder(t *testing.T) {
	g := costGraph(t, "  steps:\n    - {id: a, kind: delegate, role: ghost, prompt: 'ghp_abcdef'}\n"+
		"    - id: l\n      kind: loop\n      body: {kind: delegate, role: developer, prompt: hi}\n")
	env := authorityTestEnv()
	res := Explain(g, env)

	if got, want := codeList(res.Diagnostics), seq(CodeWF030, CodeWF036, CodeWF034); got != want {
		t.Errorf("Explain code order = %q, want %q (authority family then cost family)", got, want)
	}
	combined := append(Authority(g, env), Cost(g, env)...)
	if got, want := codeList(combined), codeList(res.Diagnostics); got != want {
		t.Errorf("Explain diagnostics %q diverge from Authority+Cost %q", want, got)
	}
	if res.Cost.Dispatches != 2 || res.Cost.Complete {
		t.Errorf("Cost = %+v, want 2 dispatches and an incomplete bound", res.Cost)
	}
	if strings.Join(res.Cost.Unbound, " ") != "steps.l" {
		t.Errorf("Unbound = %v, want [steps.l]", res.Cost.Unbound)
	}
	if len(res.Authority.Grants) != 0 || res.Authority.Unreachable != 0 {
		t.Errorf("Authority = %+v, want no grants and nothing unreachable", res.Authority)
	}
}

func TestExplainAndAggregatesOnNilGraph(t *testing.T) {
	if got := Explain(nil, authorityTestEnv()); got.Diagnostics != nil || got.Cost.Dispatches != 0 || got.Authority.Grants != nil {
		t.Errorf("Explain(nil) = %+v, want the zero result", got)
	}
	if got := Cost(nil, Env{}); got != nil {
		t.Errorf("Cost(nil) = %v, want nil", got)
	}
}

// TestCostEstimateOnGoldenCorpus pins the worst-case numbers the accepted
// documents produce, which is the assertion that keeps the formula honest:
// a rule that silently stopped counting fan-out items would still sail
// through a zero-findings test. fanout_settle is the incomplete case — both
// its fan-outs resolve their item list at run time, so 10 is a floor — and
// the budgets the goldens declare (12 and 24) are the only dispatch bounds
// the host does not supply here.
func TestCostEstimateOnGoldenCorpus(t *testing.T) {
	type expectation struct {
		dispatches int
		complete   bool
	}
	want := map[string]expectation{
		"bounded_repair": {dispatches: 7, complete: true},
		"fanout_settle":  {dispatches: 10, complete: false},
		"sequential":     {dispatches: 1, complete: true},
		"speculative":    {dispatches: 4, complete: true},
	}
	pattern := filepath.Join("..", "ir", "testdata", "specs", "*.yaml")
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("no workflow documents matched %s", pattern)
	}
	env := authorityGoldenEnv()
	seen := map[string]bool{}
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		seen[name] = true
		t.Run(name, func(t *testing.T) {
			g, diags := Validate(readFixtureFile(t, path))
			if g == nil {
				t.Fatalf("document did not resolve:%s", render(diags))
			}
			exp, ok := want[name]
			if !ok {
				t.Fatalf("no cost expectation recorded for golden %q; the table must grow with the corpus", name)
			}
			est := estimateCost(g, env)
			if est.Dispatches != exp.dispatches {
				t.Errorf("Dispatches = %d, want %d", est.Dispatches, exp.dispatches)
			}
			if est.Complete != exp.complete {
				t.Errorf("Complete = %v, want %v (Unbound %v)", est.Complete, exp.complete, est.Unbound)
			}
			if got := Cost(g, env); len(got) != 0 {
				t.Errorf("cost findings on an accepted document:%s", render(got))
			}
		})
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("golden %q disappeared from the corpus", name)
		}
	}
}

func costIntPtr(n int) *int { return &n }
