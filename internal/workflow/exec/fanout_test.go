package exec

// fanout_test.go pins fan-out execution (yaah-0u0v.14): items expand in input
// order, the collected value is assembled in that same order, a fan-out's own
// max_parallel bounds its in-flight dispatches beside the global cap, the item
// scope renders, and on_item_failure decides what a failed item does to its
// siblings. Everything runs against the FakeRuntime — the door is
// internal/workflow/host's business, and nothing here sleeps.

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// fanoutYAML builds a one-step document: a fan-out naming its `over` source,
// its per-item binding, and one delegate body node.
func fanoutYAML(name, over, extra string) string {
	return strings.Replace(specHead, "%s", name, 1) + `  steps:
    - id: f
      kind: fanout
      over: ` + over + `
      as: ws
` + extra + `      body:
        kind: delegate
        role: developer
        prompt: "work {{ ws }}"
      collect: results
`
}

// itemPathOf is the instance path of one body node, spelled the way the
// package documents it: the item discriminator between the container and the
// body index.
func itemPathOf(owner string, item, body int) string {
	return owner + ".items[" + itoa(item) + "].body[" + itoa(body) + "]"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// peakItemInFlight walks the canonical event log and reports the highest number
// of one fan-out's dispatches that were open at once. The log is the only
// witness the reducer cannot fake: every start and every commit is in it.
func peakItemInFlight(res Result, owner string) int {
	prefix := owner + ".items["
	cur, peak := 0, 0
	for _, line := range res.Events {
		kind, detail, ok := splitEvent(line)
		if !ok {
			continue
		}
		id := firstField(detail)
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		switch kind {
		case string(EventDispatchStarted):
			cur++
			if cur > peak {
				peak = cur
			}
		case string(EventAttemptCompleted), string(EventNodeCancelled):
			if cur > 0 {
				cur--
			}
		}
	}
	return peak
}

func splitEvent(line string) (kind, detail string, ok bool) {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 3 {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func firstField(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

// collectStrings renders a collected list of strings, which is what a report
// body contributes, for comparison in a test.
func collectStrings(t *testing.T, st *NodeState) []string {
	t.Helper()
	out := make([]string, 0, len(st.Collect))
	for _, v := range st.Collect {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("collected value %d is %T, want a string: %#v", len(out), v, v)
		}
		out = append(out, s)
	}
	return out
}

// TestFanoutExpandsItemsAndCollectsInInputOrder pins the base case: one body
// instance per item, the collected list in the authored order, and an identical
// event sequence across two runs.
func TestFanoutExpandsItemsAndCollectsInInputOrder(t *testing.T) {
	g := graphFromYAML(t, fanoutYAML("expand", "[alpha, beta, gamma]", ""))
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): []Completion{Report("v0", 0)},
		itemPathOf("steps.f", 1, 0): []Completion{Report("v1", 0)},
		itemPathOf("steps.f", 2, 0): []Completion{Report("v2", 0)},
	}
	res, fake := runGraph(t, g, script, Config{RunID: "run-fanout"})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	f := nodeOf(t, res, "steps.f")
	if f.State != StateSucceeded {
		t.Fatalf("fan-out state = %s, want succeeded\nlog:\n%s", f.State, logOf(res))
	}
	if len(f.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(f.Items))
	}
	for j, want := range []string{"alpha", "beta", "gamma"} {
		if f.Items[j].Index != j || f.Items[j].Bound != want {
			t.Errorf("item %d = {index %d bound %v}, want {%d %q}", j, f.Items[j].Index, f.Items[j].Bound, j, want)
		}
		if f.Items[j].State != StateSucceeded {
			t.Errorf("item %d state = %s, want succeeded", j, f.Items[j].State)
		}
		if got, want := f.Items[j].Paths, []string{itemPathOf("steps.f", j, 0)}; !reflect.DeepEqual(got, want) {
			t.Errorf("item %d paths = %v, want %v", j, got, want)
		}
	}
	if got := collectStrings(t, f); !reflect.DeepEqual(got, []string{"v0", "v1", "v2"}) {
		t.Errorf("collect = %v, want [v0 v1 v2]", got)
	}
	if f.Result != `["v0","v1","v2"]` {
		t.Errorf("recorded value = %s, want the collect encoded", f.Result)
	}
	if res.Dispatches != 3 {
		t.Errorf("dispatches = %d, want 3 (the fan-out itself dispatches nothing)", res.Dispatches)
	}
	for _, p := range startedPaths(fake) {
		if p == "steps.f" {
			t.Fatal("the fan-out node itself was dispatched — it may only expand")
		}
	}
	// The named binding is the same value the anonymous one reads.
	if len(f.Outputs) != 1 || f.Outputs[0].Name != "results" || f.Outputs[0].Value != f.Result {
		t.Errorf("output bindings = %+v, want one %q carrying the collect", f.Outputs, "results")
	}

	res2, _ := runGraph(t, g, script, Config{RunID: "run-fanout"})
	if eventLog(res) != eventLog(res2) {
		t.Error("event log differs between two identical fan-out runs — determinism broken")
	}
}

// TestFanoutCollectIgnoresCompletionAndDispatchOrder pins the collection rule
// where it can actually be observed: with twelve items the ready queue's
// canonical PATH order starts items[10] before items[2], so dispatch order and
// input order differ. The collect must still be the authored order.
func TestFanoutCollectIgnoresCompletionAndDispatchOrder(t *testing.T) {
	literals := make([]string, 12)
	script := map[string][]Completion{}
	for j := range literals {
		literals[j] = "w" + itoa(j)
		script[itemPathOf("steps.f", j, 0)] = []Completion{Report("v"+itoa(j), time.Duration(j)*time.Millisecond)}
	}
	over := "[" + strings.Join(literals, ", ") + "]"
	y := fanoutYAML("ordering", over, "      max_parallel: 1\n")
	g := graphFromYAML(t, y)

	res, fake := runGraph(t, g, script, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	want := make([]string, len(literals))
	for j := range want {
		want[j] = "v" + itoa(j)
	}
	if got := collectStrings(t, nodeOf(t, res, "steps.f")); !reflect.DeepEqual(got, want) {
		t.Errorf("collect = %v, want input order %v", got, want)
	}
	// The dispatch order really is not the input order — otherwise the
	// assertion above proves nothing.
	paths := append([]string(nil), startedPaths(fake)...)
	if sort.StringsAreSorted(paths) != true {
		t.Fatalf("dispatch order = %v, want canonical path order", paths)
	}
	// The ready queue sorts by node path, and "items[10]" < "items[2]"
	// lexicographically, so items[10] dispatches second — not third. The point
	// stands: dispatch order is path order, not input order.
	if paths[0] != itemPathOf("steps.f", 0, 0) || paths[1] != itemPathOf("steps.f", 10, 0) {
		t.Fatalf("dispatch order = %v, want items[10] to come second", paths)
	}
}

// TestFanoutMaxParallelBoundsInFlight pins the nested semaphore: a fan-out never
// has more than its max_parallel dispatches open, and the cap is what bounded
// it rather than an accident of the schedule. The second case adds the global
// ceiling on top, which the nested cap does not replace.
func TestFanoutMaxParallelBoundsInFlight(t *testing.T) {
	script := map[string][]Completion{}
	for j := 0; j < 4; j++ {
		script[itemPathOf("steps.f", j, 0)] = []Completion{Report("v"+itoa(j), 0)}
	}
	cases := []struct {
		name      string
		over      string
		extra     string
		limits    Limits
		wantPeak  int
		wantBound string
	}{
		{name: "nested cap", over: "[a, b, c, d]", extra: "      max_parallel: 2\n", wantPeak: 2, wantBound: "max_parallel"},
		{name: "nested cap under the global cap", over: "[a, b, c, d]", extra: "      max_parallel: 3\n",
			limits: Limits{MaxConcurrentDispatches: 2}, wantPeak: 2, wantBound: "max_concurrent_dispatches"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := graphFromYAML(t, fanoutYAML("cap", tc.over, tc.extra))
			res, fake := runGraph(t, g, script, Config{Limits: tc.limits})
			if res.Status != RunSucceeded {
				t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
			}
			if got := peakItemInFlight(res, "steps.f"); got != tc.wantPeak {
				t.Errorf("peak item in-flight = %d, want %d\nlog:\n%s", got, tc.wantPeak, logOf(res))
			}
			if fake.Peak() > tc.wantPeak {
				t.Errorf("door saw %d concurrent attempts, want at most %d", fake.Peak(), tc.wantPeak)
			}
			if cap := tc.limits.MaxConcurrentDispatches; cap > 0 && res.PeakInFlight > cap {
				t.Errorf("reducer peak in-flight = %d, want at most %d", res.PeakInFlight, cap)
			}
			if res.Dispatches != 4 {
				t.Errorf("dispatches = %d, want 4", res.Dispatches)
			}
		})
	}
}

// TestFanoutItemFailureContinue pins on_item_failure: continue — the failing
// item is recorded with its reason, its siblings' values survive in input order,
// and the run drains with the loss stated instead of claiming a clean success.
func TestFanoutItemFailureContinue(t *testing.T) {
	y := fanoutYAML("item-continue", "[a, b, c]", "      on_item_failure: continue\n")
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): []Completion{Report("v0", 0)},
		itemPathOf("steps.f", 1, 0): []Completion{TerminalFailure("boom", "bad input", 0)},
		itemPathOf("steps.f", 2, 0): []Completion{Report("v2", 0)},
	}
	res, _ := runGraph(t, g, script, Config{})
	if res.Status != RunFailed {
		t.Fatalf("status = %s reason=%q, want failed (an item was lost and the run says so)\nlog:\n%s",
			res.Status, res.Reason, logOf(res))
	}
	if !strings.Contains(res.Reason, itemPathOf("steps.f", 1, 0)) {
		t.Errorf("run reason %q does not name the failed item", res.Reason)
	}
	f := nodeOf(t, res, "steps.f")
	if f.State != StateSucceeded {
		t.Fatalf("fan-out state = %s, want succeeded (continue is the author's declaration)", f.State)
	}
	if got := collectStrings(t, f); !reflect.DeepEqual(got, []string{"v0", "v2"}) {
		t.Errorf("collect = %v, want [v0 v2] — successes kept, failures dropped", got)
	}
	if f.Items[1].State != StateFailed {
		t.Errorf("item 1 state = %s, want failed", f.Items[1].State)
	}
	if !strings.Contains(f.Items[1].Reason, "boom") {
		t.Errorf("item 1 reason = %q, want the envelope's stop reason", f.Items[1].Reason)
	}
	if !anyContains(res.Warnings, "1 of 3 item(s) failed") {
		t.Errorf("warnings = %v, want the failed-item count stated once", res.Warnings)
	}
}

// TestFanoutItemFailureAbortStopsRun pins the folded default: an author who
// declared nothing did not authorize the run to carry on, so a failed item fails
// the fan-out and the run stops scheduling its siblings.
func TestFanoutItemFailureAbortStopsRun(t *testing.T) {
	g := graphFromYAML(t, fanoutYAML("item-abort", "[a, b, c]", ""))
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): []Completion{Report("v0", 0)},
		itemPathOf("steps.f", 1, 0): []Completion{TerminalFailure("boom", "bad input", 0)},
		itemPathOf("steps.f", 2, 0): []Completion{Report("v2", 0)},
	}
	res, fake := runGraph(t, g, script, Config{})
	if res.Status != RunAborted {
		t.Fatalf("status = %s reason=%q, want aborted\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	f := nodeOf(t, res, "steps.f")
	if f.State != StateFailed {
		t.Fatalf("fan-out state = %s, want failed", f.State)
	}
	if !strings.Contains(f.Reason, ReasonFanoutItemFailed) || !strings.Contains(f.Reason, itemPathOf("steps.f", 1, 0)) {
		t.Errorf("fan-out reason = %q, want %s naming the item", f.Reason, ReasonFanoutItemFailed)
	}
	if len(fake.Cancels()) == 0 {
		t.Error("no attempt was cancelled — the run should stop the siblings still in flight")
	}
	if !strings.Contains(eventLog(res), string(EventNodeCancelled)) {
		t.Error("event log has no node_cancelled event for the stopped sibling")
	}
}

// TestFanoutCollectFailures pins on_item_failure: collect_failures — the failed
// item appears in the collected list at its own index, so a downstream step can
// see WHERE the gap is instead of only that the list is short.
func TestFanoutCollectFailures(t *testing.T) {
	y := fanoutYAML("item-collect-failures", "[a, b, c]", "      on_item_failure: collect_failures\n")
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): []Completion{Report("v0", 0)},
		itemPathOf("steps.f", 1, 0): []Completion{TerminalFailure("boom", "bad input", 0)},
		itemPathOf("steps.f", 2, 0): []Completion{Report("v2", 0)},
	}
	res, _ := runGraph(t, g, script, Config{})
	if res.Status != RunFailed {
		t.Fatalf("status = %s reason=%q, want failed\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	f := nodeOf(t, res, "steps.f")
	if f.State != StateSucceeded {
		t.Fatalf("fan-out state = %s, want succeeded", f.State)
	}
	if len(f.Collect) != 3 {
		t.Fatalf("collect = %#v, want three entries (two values and one failure record)", f.Collect)
	}
	if f.Collect[0] != "v0" || f.Collect[2] != "v2" {
		t.Errorf("collect = %#v, want the successes in place", f.Collect)
	}
	rec, ok := f.Collect[1].(map[string]any)
	if !ok {
		t.Fatalf("collect[1] = %T, want a failure record", f.Collect[1])
	}
	if rec["failed"] != true || rec["index"] != 1 {
		t.Errorf("failure record = %#v, want a failed entry at index 1", rec)
	}
	if !strings.Contains(rec["reason"].(string), itemPathOf("steps.f", 1, 0)) {
		t.Errorf("failure record reason = %v, want the item path", rec["reason"])
	}
}

// TestFanoutScalarOverIsOneItem pins the source-shape rule: a list is the item
// list, and a scalar — a one-element literal list, a JSON field carrying a
// single object, or a param holding a string — is ONE item rather than nothing.
func TestFanoutScalarOverIsOneItem(t *testing.T) {
	cases := []struct {
		name   string
		y      string
		params map[string]any
		script map[string][]Completion
		want   string
	}{
		{
			name: "one literal",
			y:    fanoutYAML("one-literal", "[solo]", ""),
			want: "solo",
		},
		{
			name: "single field from a producer",
			y: strings.Replace(specHead, "%s", "one-field", 1) + `  steps:
    - id: plan
      kind: delegate
      role: planner
      prompt: plan it
    - id: f
      kind: fanout
      needs: [plan]
      over: { from: steps.plan.output.workstream }
      as: ws
      body:
        kind: delegate
        role: developer
        prompt: "work {{ ws }}"
      collect: results
`,
			script: map[string][]Completion{
				"steps.plan":                {Report(`{"workstream": "solo"}`, 0)},
				itemPathOf("steps.f", 0, 0): []Completion{Report("v0", 0)},
			},
			want: "solo",
		},
		{
			name: "param holding a plain string",
			y: strings.Replace(specHead, "%s", "one-param", 1) + `  params:
    topic:
      type: string
  steps:
    - id: f
      kind: fanout
      over: { from: params.topic }
      as: ws
      body:
        kind: delegate
        role: developer
        prompt: "work {{ ws }}"
      collect: results
`,
			params: map[string]any{"topic": "solo"},
			want:   "solo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := graphFromYAML(t, tc.y)
			script := tc.script
			if script == nil {
				script = map[string][]Completion{itemPathOf("steps.f", 0, 0): []Completion{Report("v0", 0)}}
			}
			res, _ := runGraph(t, g, script, Config{Params: tc.params})
			if res.Status != RunSucceeded {
				t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
			}
			f := nodeOf(t, res, "steps.f")
			if len(f.Items) != 1 {
				t.Fatalf("items = %d, want exactly 1", len(f.Items))
			}
			if f.Items[0].Bound != tc.want {
				t.Errorf("item bound = %v, want %q", f.Items[0].Bound, tc.want)
			}
		})
	}
}

// TestFanoutZeroItemsClosesEmpty pins the honest empty case: an `over` source
// that legitimately resolves to nothing closes the fan-out with an empty collect
// and states the fact in a warning, rather than looking like a fan-out that ran.
func TestFanoutZeroItemsClosesEmpty(t *testing.T) {
	y := strings.Replace(specHead, "%s", "zero-items", 1) + `  params:
    topics:
      type: list
  steps:
    - id: f
      kind: fanout
      over: { from: params.topics }
      as: ws
      body:
        kind: delegate
        role: developer
        prompt: "work {{ ws }}"
      collect: results
`
	g := graphFromYAML(t, y)
	res, fake := runGraph(t, g, nil, Config{Params: map[string]any{"topics": []any{}}})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	f := nodeOf(t, res, "steps.f")
	if f.State != StateSucceeded || len(f.Items) != 0 {
		t.Fatalf("fan-out = {state %s items %d}, want succeeded with no items", f.State, len(f.Items))
	}
	if f.Collect == nil || len(f.Collect) != 0 {
		t.Errorf("collect = %#v, want an empty list rather than absent", f.Collect)
	}
	if f.Result != "[]" {
		t.Errorf("recorded value = %q, want []", f.Result)
	}
	if len(fake.Started()) != 0 {
		t.Errorf("dispatched %v, want nothing", startedPaths(fake))
	}
	if !anyContains(res.Warnings, "zero items") {
		t.Errorf("warnings = %v, want the empty expansion stated", res.Warnings)
	}
}

// TestFanoutQuorumCountRefused pins the declaration half of the quorum bound: a
// join: quorum with no count, or with a count below 1, is refused before a single
// item dispatches. The other joins execute as of yaah-0u0v.19, and the upper
// bound (a quorum above the item count) needs the resolved list, so it is pinned
// in join_test.go.
func TestFanoutQuorumCountRefused(t *testing.T) {
	cases := map[string]string{
		"no count":   "      join: quorum\n",
		"zero count": "      join: quorum\n      quorum: 0\n",
		"negative":   "      join: quorum\n      quorum: -2\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			g := graphFromYAML(t, fanoutYAML("join-bad-"+strings.ReplaceAll(name, " ", "-"), "[a, b]", extra))
			res, fake := runGraph(t, g, okScript(itemPathOf("steps.f", 0, 0)), Config{})
			f := nodeOf(t, res, "steps.f")
			if f.State != StateFailed {
				t.Fatalf("fan-out state = %s, want failed\nlog:\n%s", f.State, logOf(res))
			}
			if !strings.Contains(f.Reason, ReasonFanoutQuorumInvalid) {
				t.Errorf("reason = %q, want %s", f.Reason, ReasonFanoutQuorumInvalid)
			}
			if len(fake.Started()) != 0 {
				t.Errorf("dispatched %v before refusing the join — nothing may start", startedPaths(fake))
			}
			if res.Status != RunAborted {
				t.Errorf("status = %s, want aborted", res.Status)
			}
		})
	}
}

// TestFanoutOverUnavailable pins the refusal that replaced the kind fence: an
// `over` source the run cannot read fails the fan-out instead of fanning out to
// zero items, which would look like a successful run with no work.
func TestFanoutOverUnavailable(t *testing.T) {
	// A fan-out whose `over` names a field the producer never carried. The
	// producer succeeds, so the failure is the fan-out's own source, not a
	// missing kind: the kind itself executes and refuses its `over` honestly.
	y := strings.Replace(specHead, "%s", "over-unavailable", 1) + `  steps:
    - id: produce
      kind: delegate
      role: developer
      prompt: "no-op"
    - id: f
      kind: fanout
      over: { from: steps.produce.output.workstreams }
      as: ws
      body:
        kind: delegate
        role: developer
        prompt: "work {{ ws }}"
      collect: results
`
	g := graphFromYAML(t, y)
	res, _ := runGraph(t, g, okScript("steps.produce"), Config{})
	f := nodeOf(t, res, "steps.f")
	if f.State != StateFailed {
		t.Fatalf("fan-out state = %s, want failed\nlog:\n%s", f.State, logOf(res))
	}
	if !strings.Contains(f.Reason, ReasonFanoutOverUnavailable) {
		t.Errorf("reason = %q, want %s — the fan-out kind itself executes now", f.Reason, ReasonFanoutOverUnavailable)
	}
	if strings.Contains(f.Reason, ReasonNotImplemented) {
		t.Errorf("reason = %q — the fan-out kind must not refuse as not implemented", f.Reason)
	}
	if res.Status != RunAborted {
		t.Errorf("status = %s, want aborted", res.Status)
	}
}

// TestFanoutNestedChainsItemPaths pins the item-path convention for a fan-out
// nested in a fan-out's body. The inner container is itself an instance, so its
// items must splice the inner discriminator into the OUTER instance path rather
// than re-prefixing the body template path (the ".items[k].steps." corruption a
// naive instance-path prefix would produce).
func TestFanoutNestedChainsItemPaths(t *testing.T) {
	y := strings.Replace(specHead, "%s", "nested", 1) + `  steps:
    - id: outer
      kind: fanout
      over: [a, b]
      as: x
      body:
        - id: inner
          kind: fanout
          over: [1, 2]
          as: y
          body:
            kind: delegate
            role: developer
            prompt: "{{ x }}-{{ y }}"
          collect: results
      collect: results
`
	g := graphFromYAML(t, y)
	script := map[string][]Completion{}
	for oi := 0; oi < 2; oi++ {
		for ii := 0; ii < 2; ii++ {
			script[itemPathOf(itemPathOf("steps.outer", oi, 0), ii, 0)] =
				[]Completion{Report(itoa(oi)+"-"+itoa(ii), 0)}
		}
	}
	res, fake := runGraph(t, g, script, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	paths := startedPaths(fake)
	if len(paths) != 4 {
		t.Fatalf("dispatches = %d, want 4 (2 outer x 2 inner)", len(paths))
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "steps.outer.items[") {
			t.Errorf("dispatch path %s, want an outer-item prefix", p)
		}
		if strings.Contains(p, ".steps.") {
			t.Errorf("dispatch path %s re-prefixed the body template path", p)
		}
	}
}

// TestFanoutItemScopeRenders pins the per-item scope: `item.<as>` and `index`
// render for the dispatch that item owns, and the body's own templates are the
// ones compiled by ir.Resolve.
func TestFanoutItemScopeRenders(t *testing.T) {
	y := strings.Replace(specHead, "%s", "item-scope", 1) + `  steps:
    - id: f
      kind: fanout
      over: [{topic: alpha}, {topic: beta}]
      as: ws
      body:
        kind: delegate
        role: developer
        prompt: "work {{ ws.topic }} (#{{ index }})"
      collect: results
`
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): []Completion{Report("v0", 0)},
		itemPathOf("steps.f", 1, 0): []Completion{Report("v1", 0)},
	}
	res, fake := runGraph(t, g, script, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	want := []string{"work alpha (#0)", "work beta (#1)"}
	var seen int
	for _, d := range fake.Started() {
		if !strings.HasPrefix(d.Node.Path, "steps.f.items[") {
			t.Fatalf("dispatched %s, want only item instances", d.Node.Path)
		}
		item, ok := d.Scope.Item("ws")
		if !ok {
			t.Fatalf("%s: Item(ws) is unbound", d.Node.Path)
		}
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%s: item = %T, want the over element", d.Node.Path, item)
		}
		if d.Scope.Index() >= len(want) {
			t.Fatalf("%s: index = %d, out of range", d.Node.Path, d.Scope.Index())
		}
		rendered, err := d.Node.Prompt.Render(d.Scope, ir.RenderOptions{})
		if err != nil {
			t.Fatalf("%s: rendering the body prompt: %v", d.Node.Path, err)
		}
		if rendered.Text != want[d.Scope.Index()] {
			t.Errorf("%s: rendered %q, want %q", d.Node.Path, rendered.Text, want[d.Scope.Index()])
		}
		if label := m["topic"]; label != []string{"alpha", "beta"}[d.Scope.Index()] {
			t.Errorf("%s: item topic = %v", d.Node.Path, label)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("dispatched %d item instances, want 2", seen)
	}
}

// TestFanoutBodyChainStaysWithinItsItem pins that a body is a per-item sequence:
// the second body node waits for the first INSIDE its own item, and an item's
// collected value is its product — the last body node — not its first.
func TestFanoutBodyChainStaysWithinItsItem(t *testing.T) {
	y := strings.Replace(specHead, "%s", "item-chain", 1) + `  steps:
    - id: f
      kind: fanout
      over: [alpha, beta]
      as: ws
      body:
        - id: draft
          kind: delegate
          role: developer
          prompt: "draft {{ ws }}"
        - id: finish
          kind: delegate
          needs: [draft]
          role: developer
          prompt: "finish {{ steps.draft.output }}"
      collect: results
`
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): []Completion{Report("d0", 0)},
		itemPathOf("steps.f", 0, 1): []Completion{Report("p0", 0)},
		itemPathOf("steps.f", 1, 0): []Completion{Report("d1", 0)},
		itemPathOf("steps.f", 1, 1): []Completion{Report("p1", 0)},
	}
	res, fake := runGraph(t, g, script, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	f := nodeOf(t, res, "steps.f")
	if got := collectStrings(t, f); !reflect.DeepEqual(got, []string{"p0", "p1"}) {
		t.Errorf("collect = %v, want the products [p0 p1]", got)
	}
	for j := 0; j < 2; j++ {
		if got, want := f.Items[j].Paths, []string{itemPathOf("steps.f", j, 0), itemPathOf("steps.f", j, 1)}; !reflect.DeepEqual(got, want) {
			t.Errorf("item %d paths = %v, want %v", j, got, want)
		}
	}
	// Each item's draft precedes its own finish, and no item waits on another's.
	order := startedPaths(fake)
	pos := map[string]int{}
	for i, p := range order {
		pos[p] = i
	}
	for j := 0; j < 2; j++ {
		draft, finish := itemPathOf("steps.f", j, 0), itemPathOf("steps.f", j, 1)
		if pos[draft] >= pos[finish] {
			t.Errorf("item %d started %s before its need %s", j, finish, draft)
		}
	}
	// The second body node reads the first through its own item's scope.
	for _, d := range fake.Started() {
		if strings.HasSuffix(d.Node.Path, ".body[1]") {
			v, err := d.Scope.StepField("draft", "output")
			if err != nil {
				t.Errorf("%s: steps.draft.output: %v", d.Node.Path, err)
				continue
			}
			if v != "d0" && v != "d1" {
				t.Errorf("%s: draft output = %v, want its own item's draft", d.Node.Path, v)
			}
		}
	}
}

// TestFanoutCollectFeedsTheNextFanout pins that a fan-out's collect is an `over`
// source: the second fan-out waits for the first to close (a wait, not an empty
// list) and fans out over exactly the values it collected.
func TestFanoutCollectFeedsTheNextFanout(t *testing.T) {
	y := strings.Replace(specHead, "%s", "chained", 1) + `  steps:
    - id: first
      kind: fanout
      over: [alpha, beta]
      as: ws
      body:
        kind: delegate
        role: developer
        prompt: "work {{ ws }}"
      collect: made
    - id: second
      kind: fanout
      over: { from: steps.first.collect }
      as: made
      body:
        kind: delegate
        role: developer
        prompt: "review {{ made }}"
      collect: reviews
`
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		itemPathOf("steps.first", 0, 0):  []Completion{Report("m0", 0)},
		itemPathOf("steps.first", 1, 0):  []Completion{Report("m1", 0)},
		itemPathOf("steps.second", 0, 0): []Completion{Report("r0", 0)},
		itemPathOf("steps.second", 1, 0): []Completion{Report("r1", 0)},
	}
	res, _ := runGraph(t, g, script, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	first, second := nodeOf(t, res, "steps.first"), nodeOf(t, res, "steps.second")
	if got := collectStrings(t, first); !reflect.DeepEqual(got, []string{"m0", "m1"}) {
		t.Errorf("first collect = %v, want [m0 m1]", got)
	}
	if len(second.Items) != 2 {
		t.Fatalf("second items = %d, want 2 (the first fan-out's collected values)", len(second.Items))
	}
	for j, want := range []string{"m0", "m1"} {
		if second.Items[j].Bound != want {
			t.Errorf("second item %d bound = %v, want %q", j, second.Items[j].Bound, want)
		}
	}
	if got := collectStrings(t, second); !reflect.DeepEqual(got, []string{"r0", "r1"}) {
		t.Errorf("second collect = %v, want [r0 r1]", got)
	}
}

// TestFanoutCollectFeedsSettle pins the reader on the other side: a settle node
// whose proposals selector reads `steps.<id>.collect` disposes of every collected
// proposal, in input order.
func TestFanoutCollectFeedsSettle(t *testing.T) {
	y := strings.Replace(specHead, "%s", "collect-settle", 1) + `  steps:
    - id: build
      kind: fanout
      over: [alpha, beta]
      as: ws
      body:
        kind: delegate
        role: developer
        lane: proposal
        prompt: "build {{ ws }}"
      collect: proposals
    - id: land
      kind: settle
      needs: [build]
      proposals: { from: steps.build.collect }
      action: select
`
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		itemPathOf("steps.build", 0, 0): []Completion{Proposal("prop-0", 0)},
		itemPathOf("steps.build", 1, 0): []Completion{Proposal("prop-1", 0)},
	}
	res, fake := runGraph(t, g, script, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	build, land := nodeOf(t, res, "steps.build"), nodeOf(t, res, "steps.land")
	if len(build.Collect) != 2 {
		t.Fatalf("collected %d values, want 2", len(build.Collect))
	}
	if land.State != StateSucceeded || len(land.Settled) != 2 {
		t.Fatalf("land = {state %s settled %d}, want one disposition per collected proposal\nlog:\n%s",
			land.State, len(land.Settled), logOf(res))
	}
	reqs := fake.Settles()
	if len(reqs) != 2 || reqs[0].ProposalID != "prop-0" || reqs[1].ProposalID != "prop-1" {
		t.Fatalf("door calls = %+v, want prop-0 then prop-1 in input order", reqs)
	}
	for i, req := range reqs {
		if req.Producer != "steps.build" {
			t.Errorf("settle %d producer = %q, want the fan-out that collected it", i, req.Producer)
		}
	}
}

// anyContains reports whether any string in list contains substr.
func anyContains(list []string, substr string) bool {
	for _, s := range list {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
