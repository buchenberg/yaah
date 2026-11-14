package exec

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Selection tests (bead yaah-0u0v.20). Every test drives the real reducer
// through the real driver against FakeRuntime: scripted outcomes, virtual clock,
// no sleeping. What is pinned here: the judge policy's verdict plumbing,
// first_success and smallest_diff determinism and tie-breaking, the selected
// value reaching a downstream step through the ordinary scope, the refusals for
// every policy/declaration the run cannot honor, and the determinism contract.

// selectGraph resolves a document whose spec body is given.
func selectGraph(t *testing.T, spec string) *ir.Graph {
	t.Helper()
	return graphFromYAML(t, strings.Replace(specHead, "%s", "selection", 1)+spec)
}

// speculativeYAML is the reference shape: a fan-out seals two proposals and a
// select picks one of them.
const speculativeYAML = `  steps:
    - id: build
      kind: fanout
      over: [a, b]
      as: style
      max_parallel: 2
      collect: candidates
      body:
        kind: delegate
        role: developer
        lane: proposal
        prompt: 'try {{ style }}'
    - id: pick
      kind: select
      needs: [build]
      candidates: { from: steps.build.collect }
      policy: judge
      judge:
        role: reviewer
        prompt: 'pick the best candidate'
      output: winner
`

// buildPaths are the two item body instance paths speculativeYAML mints.
var buildPaths = []string{"steps.build.items[0].body[0]", "steps.build.items[1].body[0]"}

// TestSelectJudgePicksByVerdict pins the judge policy end to end: the judge
// delegate is dispatched as the select node's own attempt, its verdict names the
// winner, the node records the chosen candidate's VALUE, and nothing is settled —
// a select performs no hidden settlement, so the sealed proposals stay open.
func TestSelectJudgePicksByVerdict(t *testing.T) {
	g := selectGraph(t, speculativeYAML)
	script := map[string][]Completion{
		buildPaths[0]: {Proposal("p1", 0)},
		buildPaths[1]: {Proposal("p2", 0)},
		"steps.pick":  {Report(`{"winner":"p2","reason":"tidier"}`, 0)},
	}
	// OpenProposals is the caller's declaration that it will dispose of the
	// proposals itself: neither the winner's nor the loser's is settled here.
	res, fake := runGraph(t, g, script, Config{RunID: "sel-judge", OpenProposals: true})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s, want succeeded (reason %q)", res.Status, res.Reason)
	}
	pick := nodeOf(t, res, "steps.pick")
	if pick.State != StateSucceeded {
		t.Fatalf("pick state = %s, want succeeded (%q)", pick.State, pick.Reason)
	}
	if pick.Chosen != 1 {
		t.Errorf("chosen = %d, want 1 (the verdict named p2)", pick.Chosen)
	}
	if len(pick.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(pick.Candidates))
	}
	if got := pick.Candidates[1].Ref; got != "p2" {
		t.Errorf("candidate 1 ref = %q, want p2", got)
	}
	sel, ok := pick.Selection.(map[string]any)
	if !ok {
		t.Fatalf("selection = %T, want the winning handle mapping", pick.Selection)
	}
	if got := handleID(sel); got != "p2" {
		t.Errorf("selected proposal = %q, want p2", got)
	}
	if pick.Verdict == "" {
		t.Error("the judge's verdict body was not recorded on the node")
	}
	if pick.SelectPolicy != ir.SelectJudge {
		t.Errorf("policy = %q, want judge", pick.SelectPolicy)
	}
	// The judge is the select node's attempt, and it carries the judge's role.
	if len(pick.Attempts) != 1 {
		t.Fatalf("attempts = %d, want exactly the judge dispatch", len(pick.Attempts))
	}
	if got := pick.Attempts[0].Role; got != "reviewer" {
		t.Errorf("judge attempt role = %q, want reviewer", got)
	}
	// No hidden settlement: the run asked the door for nothing, and its ledger
	// keeps both proposals open.
	if settles := fake.Settles(); len(settles) != 0 {
		t.Errorf("select settled %d proposal(s) itself: %+v", len(settles), settles)
	}
	if strings.Contains(eventLog(res), string(EventProposalSettled)) {
		t.Error("event log carries a proposal_settled event a select must never cause")
	}
	if !strings.Contains(eventLog(res), string(EventSelectChosen)) {
		t.Error("event log has no select_chosen event")
	}
	if !strings.Contains(eventLog(res), string(EventSelectResolved)) {
		t.Error("event log has no select_resolved event")
	}
}

// TestSelectChosenValueReachesDownstream pins the plumbing, not just the ledger:
// a later step reads the winning candidate through `steps.<pick>.output.winner`
// like any other recorded value.
func TestSelectChosenValueReachesDownstream(t *testing.T) {
	g := selectGraph(t, speculativeYAML+`    - id: show
      kind: emit
      needs: [pick]
      value: '{{ steps.pick.output.winner.proposal_id }}'
`)
	script := map[string][]Completion{
		buildPaths[0]: {Proposal("p1", 0)},
		buildPaths[1]: {Proposal("p2", 0)},
		"steps.pick":  {Report(`{"winner":"p1","reason":"smallest"}`, 0)},
	}
	res, _ := runGraph(t, g, script, Config{OpenProposals: true})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s, want succeeded (reason %q)", res.Status, res.Reason)
	}
	show := nodeOf(t, res, "steps.show")
	if show.State != StateSucceeded {
		t.Fatalf("show state = %s (%q)", show.State, show.Reason)
	}
	if len(show.Outputs) != 1 || show.Outputs[0].Value != "p1" {
		t.Fatalf("rendered output = %+v, want p1", show.Outputs)
	}
}

// selectParamsYAML is a select whose candidate set is a bound parameter, so a
// test states the candidate values exactly.
const selectParamsYAML = `  params:
    cands: {type: list, required: true}
  steps:
    - id: pick
      kind: select
      candidates: { from: params.cands }
      policy: %s
      output: winner
`

// TestSelectFirstSuccess pins the deterministic policy: the first SUCCEEDED
// candidate in declared order wins, a failing earlier candidate does not take
// the prize, and the whole decision dispatches nothing.
func TestSelectFirstSuccess(t *testing.T) {
	g := selectGraph(t, strings.Replace(selectParamsYAML, "%s", "first_success", 1))
	cands := []any{
		map[string]any{"proposal_id": "p1", "status": "error", "failed": true, "reason": "no diff"},
		map[string]any{"proposal_id": "p2"},
		map[string]any{"proposal_id": "p3"},
	}
	res, fake := runGraph(t, g, nil, Config{Params: map[string]any{"cands": cands}})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s, want succeeded (reason %q)", res.Status, res.Reason)
	}
	if res.Dispatches != 0 {
		t.Errorf("dispatches = %d, want 0 (a deterministic policy dispatches nothing)", res.Dispatches)
	}
	if len(fake.Started()) != 0 {
		t.Errorf("door saw %d attempt(s); a selection over values starts none", len(fake.Started()))
	}
	pick := nodeOf(t, res, "steps.pick")
	if pick.Chosen != 1 {
		t.Errorf("chosen = %d, want 1 (candidate 0 failed)", pick.Chosen)
	}
	if got := handleID(objectOf(pick.Selection)); got != "p2" {
		t.Errorf("selected = %q, want p2", got)
	}
	if pick.Candidates[0].State != StateFailed {
		t.Errorf("candidate 0 state = %s, want failed", pick.Candidates[0].State)
	}
}

// TestSelectSmallestDiff pins the diff policy and its tie-break: fewest changed
// lines wins, and an equal count keeps the earlier candidate in declared order.
func TestSelectSmallestDiff(t *testing.T) {
	g := selectGraph(t, strings.Replace(selectParamsYAML, "%s", "smallest_diff", 1))
	cands := []any{
		map[string]any{"proposal_id": "wide", "changed_lines": 40},
		map[string]any{"proposal_id": "narrow_first", "changed_lines": 5},
		map[string]any{"proposal_id": "narrow_second", "changed_lines": 5},
		map[string]any{"proposal_id": "narrowest", "diff": map[string]any{"lines": 2}},
	}
	res, _ := runGraph(t, g, nil, Config{Params: map[string]any{"cands": cands}})
	if res.Status != RunSucceeded {
		t.Fatalf("status = %s, want succeeded (reason %q)", res.Status, res.Reason)
	}
	pick := nodeOf(t, res, "steps.pick")
	if pick.Chosen != 3 {
		t.Fatalf("chosen = %d, want 3 (2 changed lines beats 5)", pick.Chosen)
	}
	if got := handleID(objectOf(pick.Selection)); got != "narrowest" {
		t.Errorf("selected = %q, want narrowest", got)
	}
	if got := pick.Candidates[0].DiffLines; got != 40 {
		t.Errorf("candidate 0 diff_lines = %d, want 40", got)
	}

	// The tie-break is a separate run so the ordering is proved on its own.
	tie := selectGraph(t, strings.Replace(selectParamsYAML, "%s", "smallest_diff", 1))
	tieRes, _ := runGraph(t, tie, nil, Config{Params: map[string]any{"cands": []any{
		map[string]any{"proposal_id": "second", "changed_lines": 5},
		map[string]any{"proposal_id": "first", "changed_lines": 5},
	}}})
	if got := nodeOf(t, tieRes, "steps.pick").Chosen; got != 0 {
		t.Errorf("tie chose candidate %d, want 0 (declared order breaks ties)", got)
	}
}

// TestSelectRefusals pins the refusal half: every policy or declaration this run
// cannot honor ends the node terminal with a reason, dispatches nothing, and
// never hangs.
func TestSelectRefusals(t *testing.T) {
	candsSel := &ir.Selector{From: &ir.Source{Kind: ir.SourceParams, Name: "cands"}}
	handGraph := func(n *ir.Node) *ir.Graph {
		return &ir.Graph{
			Name:   "hand",
			Params: map[string]ir.Param{"cands": {Required: true}},
			Steps:  []*ir.Node{n},
		}
	}
	newNode := func() *ir.Node {
		return &ir.Node{Path: "steps.pick", ID: "pick", Kind: ir.KindSelect, Candidates: candsSel}
	}
	cases := []struct {
		name   string
		node   func() *ir.Node
		params map[string]any
		want   string
	}{
		{
			name: "unknown policy",
			node: func() *ir.Node {
				n := newNode()
				n.Policy = ir.SelectPolicy("best")
				return n
			},
			params: map[string]any{"cands": []any{map[string]any{"proposal_id": "p1"}}},
			want:   ReasonSelectPolicyUnsupported,
		},
		{
			name: "judge policy without a judge",
			node: func() *ir.Node {
				n := newNode()
				n.Policy = ir.SelectJudge
				return n
			},
			params: map[string]any{"cands": []any{map[string]any{"proposal_id": "p1"}}},
			want:   ReasonSelectJudgeMissing,
		},
		{
			name:   "empty candidate set",
			node:   newNode,
			params: map[string]any{"cands": []any{}},
			want:   ReasonSelectCandidatesEmpty,
		},
		{
			name: "no candidates selector",
			node: func() *ir.Node {
				n := newNode()
				n.Candidates = nil
				return n
			},
			params: map[string]any{"cands": []any{map[string]any{"proposal_id": "p1"}}},
			want:   ReasonSelectCandidatesUnavailable,
		},
		{
			name: "smallest_diff with no diff metadata",
			node: func() *ir.Node {
				n := newNode()
				n.Policy = ir.SelectSmallestDiff
				return n
			},
			params: map[string]any{"cands": []any{map[string]any{"proposal_id": "p1"}}},
			want:   ReasonSelectNoDiffMetadata,
		},
		{
			name: "first_success with no success",
			node: newNode,
			params: map[string]any{"cands": []any{
				map[string]any{"proposal_id": "p1", "failed": true},
			}},
			want: ReasonSelectNoCandidate,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, fake := runGraph(t, handGraph(tc.node()), nil, Config{Params: tc.params})
			node := nodeOf(t, res, "steps.pick")
			if node.State != StateFailed {
				t.Fatalf("state = %s, want failed (%q)", node.State, node.Reason)
			}
			if !strings.Contains(node.Reason, tc.want) {
				t.Errorf("reason = %q, want it to state %s", node.Reason, tc.want)
			}
			if len(fake.Started()) != 0 {
				t.Errorf("a refused declaration dispatched %d attempt(s)", len(fake.Started()))
			}
		})
	}
}

// TestSelectJudgeVerdictRefused pins the other half of the judge's contract: a
// verdict nobody can read, or one that names a candidate the judge was never
// offered, fails the node with a reason instead of silently picking candidate 0.
func TestSelectJudgeVerdictRefused(t *testing.T) {
	g := selectGraph(t, strings.Replace(selectParamsYAML, "%s", "judge", 1)+`      judge:
        role: reviewer
        prompt: pick one
`)
	params := map[string]any{"cands": []any{
		map[string]any{"proposal_id": "p1"},
		map[string]any{"proposal_id": "p2"},
	}}
	cases := map[string]struct{ verdict, want string }{
		"not json":           {`the second one looks better`, ReasonSelectVerdictInvalid},
		"no winner":          {`{"reason":"p2"}`, ReasonSelectVerdictInvalid},
		"unknown winner":     {`{"winner":"p9"}`, ReasonSelectVerdictUnmatched},
		"index out of range": {`{"winner":7}`, ReasonSelectVerdictUnmatched},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			script := map[string][]Completion{"steps.pick": {Report(tc.verdict, 0)}}
			res, _ := runGraph(t, g, script, Config{Params: params})
			node := nodeOf(t, res, "steps.pick")
			if node.State != StateFailed {
				t.Fatalf("state = %s, want failed (%q)", node.State, node.Reason)
			}
			if !strings.Contains(node.Reason, tc.want) {
				t.Errorf("reason = %q, want it to state %s", node.Reason, tc.want)
			}
		})
	}
}

// TestSelectDeterminism pins the contract the whole package is built on: the same
// graph and the same scripted outcomes yield a byte-identical event sequence, for
// the deterministic policies and for the judge alike.
func TestSelectDeterminism(t *testing.T) {
	hand := selectGraph(t, strings.Replace(selectParamsYAML, "%s", "smallest_diff", 1))
	params := map[string]any{"cands": []any{
		map[string]any{"proposal_id": "a", "changed_lines": 9},
		map[string]any{"proposal_id": "b", "changed_lines": 3},
	}}
	first, _ := runGraph(t, hand, nil, Config{Params: params})
	second, _ := runGraph(t, hand, nil, Config{Params: params})
	if eventLog(first) != eventLog(second) {
		t.Errorf("event log differs between two identical runs:\n%s\n---\n%s",
			eventLog(first), eventLog(second))
	}

	judge := selectGraph(t, speculativeYAML)
	script := map[string][]Completion{
		buildPaths[0]: {Proposal("p1", 0)},
		buildPaths[1]: {Proposal("p2", 0)},
		"steps.pick":  {Report(`{"winner":"p2"}`, 0)},
	}
	j1, _ := runGraph(t, judge, script, Config{OpenProposals: true})
	j2, _ := runGraph(t, judge, script, Config{OpenProposals: true})
	if eventLog(j1) != eventLog(j2) {
		t.Errorf("judge event log differs between two identical runs:\n%s\n---\n%s",
			eventLog(j1), eventLog(j2))
	}
}

// TestSelectNoLongerFenced pins that the refuse fence moved: a select node is no
// longer a not_implemented kind, which is why TestNonDelegateKindsRefused's golden
// now rests on the gate kind (yaah-0u0v.21) alone.
func TestSelectNoLongerFenced(t *testing.T) {
	n := &ir.Node{Path: "steps.pick", ID: "pick", Kind: ir.KindSelect}
	for _, policy := range []ir.SelectPolicy{ir.SelectFirstSuccess, ir.SelectSmallestDiff, ""} {
		nn := *n
		nn.Policy = policy
		action, note := classifyKind(&nn)
		if action == kindRefuse {
			t.Errorf("policy %q still refuses: %s", policy, note)
		}
	}
}
