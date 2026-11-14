package exec

// join_test.go pins join: any and join: quorum execution (yaah-0u0v.19).
//
// What is specific to the two count-based joins, and therefore what these tests
// are about:
//
//   - the WINNER SET is fixed when the join is satisfied, so the collected value
//     is the winners' values and nothing that arrives later widens it;
//   - everything the close abandons is DRAINED (an attempt the door already
//     finished is recorded) or CANCELLED through Runtime.Cancel, never left
//     running — which is why the fake has a Hold hook: an ordinary scripted
//     attempt completes the instant it starts, so a cancellation test needs
//     attempts that stay in flight;
//   - a proposal a NON-WINNING item sealed is discarded through the door's
//     settlement path, and the winner's proposal is left to the graph;
//   - the caps hold and no non-winning item's proposal is left open, for every
//     combination of item count, join, quorum, max_parallel, and global cap.
//
// Everything runs against the FakeRuntime: the door is internal/workflow/host's
// business, and nothing here sleeps.

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// runJoin executes g on a fake whose attempts for the named paths stay in flight,
// failing the test on transport errors. runGraph is the shared helper; a
// cancellation test additionally needs work that has NOT finished, which is what
// Hold scripts.
func runJoin(t *testing.T, g *ir.Graph, script map[string][]Completion, hold []string, cfg Config) (Result, *FakeRuntime) {
	t.Helper()
	rt := NewFakeRuntime(time.Unix(0, 0))
	rt.Script = script
	if len(hold) > 0 {
		rt.Hold = make(map[string]bool, len(hold))
		for _, p := range hold {
			rt.Hold[p] = true
		}
	}
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

// proposalYAML builds a one-step proposal-lane fan-out: every item seals a
// proposal instead of reporting a value, which is the shape a speculative
// join: any race has in practice.
func proposalYAML(name, over, extra string) string {
	return strings.Replace(specHead, "%s", name, 1) + `  steps:
    - id: f
      kind: fanout
      over: ` + over + `
      as: ws
` + extra + `      body:
        kind: delegate
        lane: proposal
        role: developer
        prompt: "work {{ ws }}"
      collect: results
`
}

// dispatchOf is the dispatch id of an item's single body node, the identity the
// door's cancel calls are logged under.
func dispatchOf(owner string, item, body, attempt int) string {
	return itemPathOf(owner, item, body) + "#" + itoa(attempt)
}

// settledProposals reports the proposal ids the door was asked to dispose of with
// the given verb, in request order.
func settledProposals(fake *FakeRuntime, verb string) []string {
	var out []string
	for _, s := range fake.Settles() {
		if s.Verb == verb {
			out = append(out, s.ProposalID)
		}
	}
	return out
}

// TestFanoutJoinAnyFirstSuccessWins pins the base case of join: any: the first
// item to succeed closes the container, the collected value is that item's alone,
// and every sibling still in flight is cancelled through the door — no attempt is
// left running and no dispatch is started for work nobody will collect.
func TestFanoutJoinAnyFirstSuccessWins(t *testing.T) {
	g := graphFromYAML(t, fanoutYAML("join-any", "[a, b, c]", "      join: any\n"))
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): {Report("v0", 0)},
		itemPathOf("steps.f", 1, 0): {Report("v1", 0)},
		itemPathOf("steps.f", 2, 0): {Report("v2", 0)},
	}
	hold := []string{itemPathOf("steps.f", 1, 0), itemPathOf("steps.f", 2, 0)}
	res, fake := runJoin(t, g, script, hold, Config{RunID: "run-join-any"})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	f := nodeOf(t, res, "steps.f")
	if f.State != StateSucceeded {
		t.Fatalf("fan-out state = %s, want succeeded\nlog:\n%s", f.State, logOf(res))
	}
	if got, want := f.Winners, []int{0}; !reflect.DeepEqual(got, want) {
		t.Errorf("winners = %v, want %v", got, want)
	}
	if got := collectStrings(t, f); !reflect.DeepEqual(got, []string{"v0"}) {
		t.Errorf("collect = %v, want the winner's value alone", got)
	}
	if f.Result != `["v0"]` {
		t.Errorf("recorded value = %s, want [\"v0\"]", f.Result)
	}
	if f.Items[1].State != StateCancelled || f.Items[2].State != StateCancelled {
		t.Errorf("item states = %s, %s, want both cancelled", f.Items[1].State, f.Items[2].State)
	}
	if !strings.Contains(f.Items[1].Reason, ReasonFanoutJoinAbandoned) {
		t.Errorf("cancelled item reason = %q, want %s", f.Items[1].Reason, ReasonFanoutJoinAbandoned)
	}
	cancels := append([]string(nil), fake.Cancels()...)
	sort.Strings(cancels)
	want := []string{dispatchOf("steps.f", 1, 0, 1), dispatchOf("steps.f", 2, 0, 1)}
	sort.Strings(want)
	if !reflect.DeepEqual(cancels, want) {
		t.Errorf("door cancels = %v, want %v (every loser stopped, nothing left running)", cancels, want)
	}
	if fake.InFlight() != 0 {
		t.Errorf("door still holds %d in-flight attempt(s) after the run — work was orphaned", fake.InFlight())
	}
	if res.Dispatches != 3 {
		t.Errorf("dispatches = %d, want 3 (the fan-out itself dispatches nothing)", res.Dispatches)
	}
	if !strings.Contains(eventLog(res), string(EventNodeCancelled)) {
		t.Error("event log has no node_cancelled event for the losers")
	}
	// Determinism is part of the contract for every join.
	res2, _ := runJoin(t, g, script, hold, Config{RunID: "run-join-any"})
	if eventLog(res) != eventLog(res2) {
		t.Error("event log differs between two identical join: any runs — determinism broken")
	}
}

// TestFanoutJoinQuorumReturnsAfterNSuccesses pins join: quorum: the container
// closes on the Nth success, the collect is those N values in item order, and the
// items still in flight are cancelled.
func TestFanoutJoinQuorumReturnsAfterNSuccesses(t *testing.T) {
	y := fanoutYAML("join-quorum", "[a, b, c, d]", "      join: quorum\n      quorum: 2\n")
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): {Report("v0", 0)},
		itemPathOf("steps.f", 1, 0): {Report("v1", 0)},
		itemPathOf("steps.f", 2, 0): {Report("v2", 0)},
		itemPathOf("steps.f", 3, 0): {Report("v3", 0)},
	}
	hold := []string{itemPathOf("steps.f", 2, 0), itemPathOf("steps.f", 3, 0)}
	res, fake := runJoin(t, g, script, hold, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	f := nodeOf(t, res, "steps.f")
	if got, want := f.Winners, []int{0, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("winners = %v, want %v", got, want)
	}
	if got := collectStrings(t, f); !reflect.DeepEqual(got, []string{"v0", "v1"}) {
		t.Errorf("collect = %v, want [v0 v1]", got)
	}
	if f.Items[2].State != StateCancelled || f.Items[3].State != StateCancelled {
		t.Errorf("item states = %s, %s, want both cancelled", f.Items[2].State, f.Items[3].State)
	}
	if got := len(fake.Cancels()); got != 2 {
		t.Errorf("door cancels = %d (%v), want 2", got, fake.Cancels())
	}
	if fake.InFlight() != 0 {
		t.Errorf("door still holds %d in-flight attempt(s) — work was orphaned", fake.InFlight())
	}
}

// TestFanoutJoinLateResultIsRecordedButNotCounted pins the rule the bead states
// literally: a result that arrives after the join was decided is RECORDED on its
// own item and never folded into the container. It is reachable without the Hold
// hook because the fake completes an attempt as soon as it starts, so the sibling
// results exist at the door and are drained instead of cancelled.
func TestFanoutJoinLateResultIsRecordedButNotCounted(t *testing.T) {
	g := graphFromYAML(t, fanoutYAML("join-late", "[a, b, c]", "      join: any\n"))
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): {Report("v0", 0)},
		itemPathOf("steps.f", 1, 0): {Report("v1", 0)},
		itemPathOf("steps.f", 2, 0): {Report("v2", 0)},
	}
	res, fake := runJoin(t, g, script, nil, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	f := nodeOf(t, res, "steps.f")
	if got, want := f.Winners, []int{0}; !reflect.DeepEqual(got, want) {
		t.Errorf("winners = %v, want %v — a late success may not join the winner set", got, want)
	}
	if got := collectStrings(t, f); !reflect.DeepEqual(got, []string{"v0"}) {
		t.Errorf("collect = %v, want [v0]; a late result must not widen it", got)
	}
	// The late results ARE on their items: they ran, and the record says so.
	for j := 1; j < 3; j++ {
		it := f.Items[j]
		if it.State != StateSucceeded {
			t.Errorf("item %d state = %s, want succeeded (the work was drained, not lost)", j, it.State)
		}
		if got, want := it.Value, "v"+itoa(j); got != want {
			t.Errorf("item %d value = %v, want %q", j, got, want)
		}
		if it.Value != nil && it.Value == f.Collect[0] {
			t.Errorf("item %d value leaked into the collect", j)
		}
	}
	if len(f.Collect) != 1 {
		t.Errorf("collect = %#v, want exactly the one winner", f.Collect)
	}
	// Draining is not cancelling: nothing was asked to stop.
	if got := fake.Cancels(); len(got) != 0 {
		t.Errorf("door cancels = %v, want none (a finished attempt is recorded, not cancelled)", got)
	}
	log := eventLog(res)
	for j := 1; j < 3; j++ {
		if !strings.Contains(log, dispatchOf("steps.f", j, 0, 1)+" attempt=1") {
			t.Errorf("event log does not record the late completion of item %d\nlog:\n%s", j, log)
		}
	}
}

// TestFanoutJoinQuorumUnreachableFails pins the count the run must not wait for: a
// quorum that the terminated items have already disproved fails the container and
// names the shortfall, instead of hanging on items that cannot make the count.
func TestFanoutJoinQuorumUnreachableFails(t *testing.T) {
	y := fanoutYAML("join-unmet", "[a, b, c, d]", "      join: quorum\n      quorum: 3\n")
	g := graphFromYAML(t, y)
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): {TerminalFailure("boom", "bad input", 0)},
		itemPathOf("steps.f", 1, 0): {TerminalFailure("boom", "bad input", 0)},
		itemPathOf("steps.f", 2, 0): {Report("v2", 0)},
		itemPathOf("steps.f", 3, 0): {Report("v3", 0)},
	}
	res, _ := runJoin(t, g, script, nil, Config{})
	if res.Status != RunAborted {
		t.Fatalf("status = %s reason=%q, want aborted (the container failed under the default on_failure)\nlog:\n%s",
			res.Status, res.Reason, logOf(res))
	}
	f := nodeOf(t, res, "steps.f")
	if f.State != StateFailed {
		t.Fatalf("fan-out state = %s, want failed", f.State)
	}
	if !strings.Contains(f.Reason, ReasonFanoutJoinUnmet) || !strings.Contains(f.Reason, "cannot be satisfied") {
		t.Errorf("reason = %q, want %s naming the shortfall", f.Reason, ReasonFanoutJoinUnmet)
	}
	if !strings.Contains(f.Reason, "quorum 3") {
		t.Errorf("reason = %q, want the declared quorum count", f.Reason)
	}
}

// TestFanoutJoinQuorumAboveItemCountRefused pins the upper half of the quorum
// bound: a quorum the resolved item list cannot satisfy is refused at expansion,
// before a single item dispatches, rather than becoming a container that fails
// halfway.
func TestFanoutJoinQuorumAboveItemCountRefused(t *testing.T) {
	y := fanoutYAML("join-too-many", "[a, b]", "      join: quorum\n      quorum: 3\n")
	g := graphFromYAML(t, y)
	res, fake := runJoin(t, g, okScript(itemPathOf("steps.f", 0, 0)), nil, Config{})
	f := nodeOf(t, res, "steps.f")
	if f.State != StateFailed {
		t.Fatalf("fan-out state = %s, want failed\nlog:\n%s", f.State, logOf(res))
	}
	if !strings.Contains(f.Reason, ReasonFanoutQuorumInvalid) {
		t.Errorf("reason = %q, want %s", f.Reason, ReasonFanoutQuorumInvalid)
	}
	if len(fake.Started()) != 0 {
		t.Errorf("dispatched %v before refusing the quorum — nothing may start", startedPaths(fake))
	}
	if res.Status != RunAborted {
		t.Errorf("status = %s, want aborted", res.Status)
	}
}

// TestFanoutJoinAnyEmptyItemsRefused pins the honest empty case for a count-based
// join: zero items satisfies join: all vacuously, but no item can ever supply the
// success any or quorum needs, so the container says so.
func TestFanoutJoinAnyEmptyItemsRefused(t *testing.T) {
	y := strings.Replace(specHead, "%s", "join-empty", 1) + `  params:
    topics:
      type: list
  steps:
    - id: f
      kind: fanout
      over: { from: params.topics }
      as: ws
      join: any
      body:
        kind: delegate
        role: developer
        prompt: "work {{ ws }}"
      collect: results
`
	g := graphFromYAML(t, y)
	res, fake := runJoin(t, g, nil, nil, Config{Params: map[string]any{"topics": []any{}}})
	f := nodeOf(t, res, "steps.f")
	if f.State != StateFailed {
		t.Fatalf("fan-out state = %s, want failed\nlog:\n%s", f.State, logOf(res))
	}
	if !strings.Contains(f.Reason, ReasonFanoutJoinUnmet) {
		t.Errorf("reason = %q, want %s", f.Reason, ReasonFanoutJoinUnmet)
	}
	if len(fake.Started()) != 0 {
		t.Errorf("dispatched %v with no items", startedPaths(fake))
	}
}

// TestFanoutJoinDiscardsLosingItemProposals pins the proposal half of the bead's
// acceptance: a proposal a NON-WINNING item sealed is discarded through the door's
// settlement path, and the winner's proposal is left to the graph. The two runs
// make the split observable from both sides — the disposing run reports no residue
// for the losers, and the run that declines to state a disposition for the winner
// names exactly the winner's proposal and no other.
func TestFanoutJoinDiscardsLosingItemProposals(t *testing.T) {
	g := graphFromYAML(t, proposalYAML("join-discard", "[a, b, c]", "      join: any\n"))
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): {Proposal("p0", 0)},
		itemPathOf("steps.f", 1, 0): {Proposal("p1", 0)},
		itemPathOf("steps.f", 2, 0): {Proposal("p2", 0)},
	}

	t.Run("losers discarded, winner left to the graph", func(t *testing.T) {
		res, fake := runJoin(t, g, script, nil, Config{OpenProposals: true})
		if res.Status != RunSucceeded {
			t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
		}
		f := nodeOf(t, res, "steps.f")
		if got, want := f.Winners, []int{0}; !reflect.DeepEqual(got, want) {
			t.Errorf("winners = %v, want %v", got, want)
		}
		if got, want := settledProposals(fake, "discard"), []string{"p1", "p2"}; !reflect.DeepEqual(got, want) {
			t.Errorf("discarded proposals = %v, want %v", got, want)
		}
		if got := settledProposals(fake, "apply"); len(got) != 0 {
			t.Errorf("applied proposals = %v, want none — a cancelled item is not landed", got)
		}
		if len(f.Discards) != 2 {
			t.Fatalf("container discards = %+v, want two records", f.Discards)
		}
		for _, s := range f.Discards {
			if s.Status != SettlementStatusSettled || s.Action != "discard" || s.LeftOpen {
				t.Errorf("discard record = %+v, want a settled discard", s)
			}
		}
		// The winner's proposal is NOT the container's business: it is still open,
		// waiting for whatever the graph declared.
		for _, s := range f.Discards {
			if s.ProposalID == "p0" {
				t.Error("the winning item's proposal was discarded — the join's own work was thrown away")
			}
		}
		if got := len(fake.Settles()); got != 2 {
			t.Errorf("settlement requests = %d (%+v), want exactly the two losers", got, fake.Settles())
		}
	})

	t.Run("residue stated when the winner is undisposed", func(t *testing.T) {
		res, fake := runJoin(t, g, script, nil, Config{})
		if res.Status != RunFailed {
			t.Fatalf("status = %s reason=%q, want failed\nlog:\n%s", res.Status, res.Reason, logOf(res))
		}
		if !strings.Contains(res.Reason, ReasonOpenProposalsAtExit) {
			t.Errorf("run reason = %q, want %s", res.Reason, ReasonOpenProposalsAtExit)
		}
		if !strings.Contains(res.Reason, "p0") {
			t.Errorf("run reason = %q, want the winner's proposal named", res.Reason)
		}
		for _, id := range []string{"p1", "p2"} {
			if strings.Contains(res.Reason, id) {
				t.Errorf("run reason = %q, want only the winner's residue; %s was discarded", res.Reason, id)
			}
		}
		if got, want := settledProposals(fake, "discard"), []string{"p1", "p2"}; !reflect.DeepEqual(got, want) {
			t.Errorf("discarded proposals = %v, want %v", got, want)
		}
	})
}

// TestFanoutJoinCancelledItemProposalNeverSealed pins the other proposal case: an
// item stopped while running sealed nothing (there is no envelope), so there is
// nothing to discard — a cancelled item leaves no proposal behind because it never
// produced one.
func TestFanoutJoinCancelledItemProposalNeverSealed(t *testing.T) {
	g := graphFromYAML(t, proposalYAML("join-cancel-proposal", "[a, b]", "      join: any\n"))
	script := map[string][]Completion{
		itemPathOf("steps.f", 0, 0): {Proposal("p0", 0)},
		itemPathOf("steps.f", 1, 0): {Proposal("p1", 0)},
	}
	hold := []string{itemPathOf("steps.f", 1, 0)}
	res, fake := runJoin(t, g, script, hold, Config{OpenProposals: true})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	if got := settledProposals(fake, "discard"); len(got) != 0 {
		t.Errorf("discarded proposals = %v, want none — the cancelled item never reported a proposal", got)
	}
	if f := nodeOf(t, res, "steps.f"); f.Items[1].State != StateCancelled {
		t.Errorf("item 1 state = %s, want cancelled", f.Items[1].State)
	}
}

// TestFanoutJoinNestedCancelJoinStaysInsideItsItem pins that the decision and its
// cancellation are scoped: an inner join: any inside an outer join: all item
// cancels its OWN losers, and the outer container still collects both items.
func TestFanoutJoinNestedCancelJoinStaysInsideItsItem(t *testing.T) {
	y := strings.Replace(specHead, "%s", "join-nested", 1) + `  steps:
    - id: outer
      kind: fanout
      over: [x, y]
      as: o
      body:
        - id: inner
          kind: fanout
          over: [1, 2]
          as: i
          join: any
          body:
            kind: delegate
            role: developer
            prompt: "{{ o }}-{{ i }}"
          collect: picked
      collect: results
`
	g := graphFromYAML(t, y)
	var inner []string
	script := map[string][]Completion{}
	for oi := 0; oi < 2; oi++ {
		innerPath := itemPathOf("steps.outer", oi, 0)
		for ii := 0; ii < 2; ii++ {
			p := itemPathOf(innerPath, ii, 0)
			script[p] = []Completion{Report(itoa(oi)+"-"+itoa(ii), 0)}
			if ii == 1 {
				inner = append(inner, p)
			}
		}
	}
	res, fake := runJoin(t, g, script, inner, Config{})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	for oi := 0; oi < 2; oi++ {
		innerPath := itemPathOf("steps.outer", oi, 0)
		ic := nodeOf(t, res, innerPath)
		if ic.State != StateSucceeded {
			t.Fatalf("inner container %s state = %s, want succeeded\nlog:\n%s", innerPath, ic.State, logOf(res))
		}
		if got, want := ic.Winners, []int{0}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s winners = %v, want %v", innerPath, got, want)
		}
		if ic.Items[1].State != StateCancelled {
			t.Errorf("%s item 1 state = %s, want cancelled", innerPath, ic.Items[1].State)
		}
	}
	// Exactly the two held inner siblings were cancelled: the outer container's
	// items are not the inner join's to cancel. The door records cancels by
	// dispatch id, and each inner loser made exactly one attempt.
	cancels := append([]string(nil), fake.Cancels()...)
	want := make([]string, 0, len(inner))
	for _, p := range inner {
		want = append(want, dispatchID(p, 1))
	}
	sort.Strings(cancels)
	sort.Strings(want)
	if !reflect.DeepEqual(cancels, want) {
		t.Errorf("door cancels = %v, want the inner losers %v", cancels, want)
	}
	if fake.InFlight() != 0 {
		t.Errorf("door still holds %d in-flight attempt(s)", fake.InFlight())
	}
}

// TestFanoutJoinPropertyCapsAndProposalCleanup pins the two properties the bead
// names, over the whole small grid rather than over random samples, so a failure
// names the exact case. For every item count, join, quorum, max_parallel, and
// global concurrency cap:
//
//   - neither the nested nor the global cap is ever exceeded, and the reducer's
//     own witness (PeakInFlight) agrees;
//   - the winner set is exactly as large as the join requires and the collect is
//     exactly the winners' values;
//   - every proposal a NON-winning item sealed is disposed of with the discard
//     verb, and no winner's proposal is discarded;
//   - two identical runs produce byte-identical event logs.
func TestFanoutJoinPropertyCapsAndProposalCleanup(t *testing.T) {
	for items := 1; items <= 4; items++ {
		for _, join := range []string{"any", "quorum"} {
			needs := []int{1}
			if join == "quorum" {
				needs = nil
				for q := 1; q <= items; q++ {
					needs = append(needs, q)
				}
			}
			for _, need := range needs {
				for _, mp := range []int{0, 2} {
					for _, cap := range []int{0, 2} {
						name := "items=" + itoa(items) + "/" + join + "=" + itoa(need) +
							"/max_parallel=" + itoa(mp) + "/cap=" + itoa(cap)
						t.Run(name, func(t *testing.T) {
							assertJoinProperty(t, items, join, need, mp, cap)
						})
					}
				}
			}
		}
	}
}

// assertJoinProperty runs one case of the property grid and checks the invariants
// that must hold for every join: any and quorum.
func assertJoinProperty(t *testing.T, items int, join string, need, maxParallel, concCap int) {
	t.Helper()
	literals := make([]string, items)
	script := make(map[string][]Completion, items)
	for j := 0; j < items; j++ {
		literals[j] = "w" + itoa(j)
		script[itemPathOf("steps.f", j, 0)] = []Completion{Proposal("p"+itoa(j), time.Duration(j)*time.Millisecond)}
	}
	extra := "      join: " + join + "\n"
	if join == "quorum" {
		extra += "      quorum: " + itoa(need) + "\n"
	}
	if maxParallel > 0 {
		extra += "      max_parallel: " + itoa(maxParallel) + "\n"
	}
	g := graphFromYAML(t, proposalYAML("join-property", "["+strings.Join(literals, ", ")+"]", extra))
	cfg := Config{Limits: Limits{MaxConcurrentDispatches: concCap}, OpenProposals: true}

	res, fake := runJoin(t, g, script, nil, cfg)
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s reason=%q\nlog:\n%s", res.Status, res.Reason, logOf(res))
	}
	f := nodeOf(t, res, "steps.f")

	// The winner set is exactly what the join requires, and the collect is exactly
	// the winners' values.
	if len(f.Winners) != need {
		t.Errorf("winners = %v (%d), want %d", f.Winners, len(f.Winners), need)
	}
	if len(f.Collect) != need {
		t.Errorf("collect = %#v, want %d value(s)", f.Collect, need)
	}
	for _, j := range f.Winners {
		if f.Items[j].State != StateSucceeded {
			t.Errorf("winner %d state = %s, want succeeded for a joined item", j, f.Items[j].State)
		}
	}

	// Neither cap is ever exceeded, and the reducer's own witness agrees.
	if concCap > 0 && res.PeakInFlight > concCap {
		t.Errorf("reducer peak in-flight = %d, want at most the global cap %d", res.PeakInFlight, concCap)
	}
	if fake.Peak() > effectiveCap(items, maxParallel, concCap) {
		t.Errorf("door saw %d concurrent attempts, want at most %d",
			fake.Peak(), effectiveCap(items, maxParallel, concCap))
	}
	if got := peakItemInFlight(res, "steps.f"); got > effectiveCap(items, maxParallel, concCap) {
		t.Errorf("peak item in-flight = %d, want at most %d\nlog:\n%s",
			got, effectiveCap(items, maxParallel, concCap), logOf(res))
	}

	// No proposal a NON-winning item sealed is left open, and no winner's proposal
	// is thrown away. An item the run never dispatched sealed nothing, so it has no
	// proposal to dispose of.
	dispatched := map[string]bool{}
	for _, p := range startedPaths(fake) {
		dispatched[p] = true
	}
	discarded := map[string]bool{}
	for _, id := range settledProposals(fake, "discard") {
		discarded[id] = true
	}
	for j := 0; j < items; j++ {
		id := "p" + itoa(j)
		if !dispatched[itemPathOf("steps.f", j, 0)] {
			if discarded[id] {
				t.Errorf("proposal %s was discarded but item %d never ran", id, j)
			}
			continue
		}
		if containsInt(f.Winners, j) {
			if discarded[id] {
				t.Errorf("the winning item %d had its proposal %s discarded", j, id)
			}
			continue
		}
		if !discarded[id] {
			t.Errorf("proposal %s sealed by non-winning item %d is still open (settles=%+v)",
				id, j, fake.Settles())
		}
	}

	// The same graph and the same scripted outcomes produce the same log.
	res2, _ := runJoin(t, g, script, nil, cfg)
	if eventLog(res) != eventLog(res2) {
		t.Errorf("event log differs between two identical runs\nfirst:\n%s\nsecond:\n%s",
			eventLog(res), eventLog(res2))
	}
}

// effectiveCap is the tightest ceiling one item's dispatch can meet: the fan-out's
// own max_parallel, the run's global cap, and the item count, folded to the
// smallest declared one.
func effectiveCap(items, maxParallel, concCap int) int {
	out := items
	if maxParallel > 0 && maxParallel < out {
		out = maxParallel
	}
	if concCap > 0 && concCap < out {
		out = concCap
	}
	return out
}
