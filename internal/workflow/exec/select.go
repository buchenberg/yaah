package exec

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Candidate selection (yaah-0u0v.20).
//
// A select node chooses ONE candidate out of a set a producer already
// collected — typically a fan-out's `collect`, whose elements are the sealed
// proposal handles of the speculative attempts. It is a thin node: it dispatches
// nothing of its own except, under `policy: judge`, the one verdict-producing
// delegate the document declared, and it settles nothing at all. The winner is
// recorded as the node's VALUE, so `steps.<pick>.output.winner` is what a
// downstream settle applies; the losing candidates' proposals stay open and the
// run-level ledger keeps refusing to report success over them until a settle
// disposes of each. That is the whole reason this node may not settle: a pick
// that silently discarded its losers would throw away worktrees nobody asked it
// to touch.
//
// # Where the candidates come from
//
// `candidates` is an ordinary selector, so it resolves through the same value
// extraction a template and a settle handle set read (Run.selectorValue). The
// resolved value is normalized into a list with the fan-out rule
// (elementsFromValue): a list is the candidate list in its own order, a single
// mapping is one candidate, and nil is the empty set — which is refused rather
// than reported as a selection over nothing. The optional predicate filters the
// list element-wise, exactly as an `over` does.
//
// # How a candidate is read
//
// A candidate element is a VALUE, and the selection policies read facts out of
// it (see Candidate). Nothing here re-derives a node's lifecycle from the graph:
// an element that names a node in this run (`node:` or `path:`, or a bare step
// id) is correlated with that node's state, because that is the strongest fact
// available, and an element that names none is read from its own fields —
// `failed: true` is the record on_item_failure: collect_failures writes in place
// of a lost item, and `status: error` is an envelope status.
//
// # Policies
//
//   - first_success takes the first SUCCEEDED candidate in declared order;
//   - smallest_diff takes the candidate with the fewest changed lines, breaking
//     ties by declared order, and refuses when no eligible candidate carries a
//     diff fact at all — falling back to another policy would hide that the
//     policy's whole input was missing;
//   - judge dispatches the declared judge delegate as an attempt OF THIS NODE and
//     reads the structured verdict it delivers. The verdict names the winner by
//     candidate index or by the candidate's ref (a proposal id, a node path, or a
//     step id); a verdict naming nobody in the set is refused, never ignored.
//
// An undeclared policy is first_success: the deterministic reading needs no
// extra dispatch and carries no authority the author never wrote, and a default
// that dispatched a model would make an omitted `policy:` cost a run.
//
// # What this file does not do
//
// It starts no goroutine, takes no lock, and asks the door for nothing but the
// judge's attempt — which is an ordinary dispatch, so it goes through the
// write-ahead dispatch_started record, the concurrency cap, the budget axes, and
// the retry rule like any other attempt.

// Reasons the selection family states.
const (
	// ReasonSelectCandidatesUnavailable prefixes a `candidates` set this run
	// cannot resolve: no selector at all, an unreadable source, or a predicate
	// no element satisfies. An unresolvable set is not an empty one, and both
	// are refused rather than reported as a selection over nothing.
	ReasonSelectCandidatesUnavailable = "select_candidates_unavailable"
	// ReasonSelectCandidatesEmpty prefixes a set that resolved to zero
	// candidates: no policy can choose among nothing. WF028 reports the half a
	// document can prove; this is the run-time half.
	ReasonSelectCandidatesEmpty = "select_candidates_empty"
	// ReasonSelectJudgeMissing names `policy: judge` with no judge payload:
	// nobody is named to decide, so the selection has no rule.
	ReasonSelectJudgeMissing = "select_judge_missing"
	// ReasonSelectPolicyUnsupported names a policy outside the three the
	// vocabulary declares. Only a hand-built graph reaches it — ir.Resolve
	// refuses an unknown policy with a resolve error — so it is stated as a
	// refusal rather than left to crash.
	ReasonSelectPolicyUnsupported = "select_policy_unsupported"
	// ReasonSelectNoCandidate prefixes a set in which no candidate qualifies:
	// none succeeded, or none carried the fact the policy reads.
	ReasonSelectNoCandidate = "select_no_candidate"
	// ReasonSelectNoDiffMetadata names smallest_diff over a set where no
	// eligible candidate records a changed-line count.
	ReasonSelectNoDiffMetadata = "select_no_diff_metadata"
	// ReasonSelectVerdictInvalid prefixes a judge result that is not a
	// structured verdict at all: no JSON object, or no winner field.
	ReasonSelectVerdictInvalid = "select_verdict_invalid"
	// ReasonSelectVerdictUnmatched names a verdict that picked nobody in the
	// candidate set — an index out of range, or a ref no candidate carries.
	ReasonSelectVerdictUnmatched = "select_verdict_unmatched"
)

// Candidate is one element of a select node's resolved candidate set, as the
// reducer read it. It is the ledger a policy decides over and a trace quotes, so
// it records the facts the decision used rather than the decision itself.
type Candidate struct {
	// Index is the candidate's position in the resolved list — declared order,
	// and the tie-break every policy falls back to.
	Index int `json:"index"`
	// Ref is the candidate's identity as a verdict names it: the proposal id of
	// a sealed handle, else the id/node/step the element carries.
	Ref string `json:"ref,omitempty"`
	// Node is the node path this candidate was correlated with, when the element
	// named one in this run.
	Node string `json:"node,omitempty"`
	// State is the candidate's status. It is the correlated node's state when one
	// was found, and otherwise StateSucceeded for a plain value and StateFailed
	// for a failure record.
	State State `json:"state,omitempty"`
	// DiffLines is the changed-line count the candidate carries, or -1 when it
	// carries none.
	DiffLines int `json:"diff_lines"`
	// Value is the candidate itself: what the node reports if this one wins.
	Value any `json:"value,omitempty"`
	// Reason is the candidate's own stated cause, when it has one.
	Reason string `json:"reason,omitempty"`
}

// selectPolicyOf folds the one default the selection family owns: an author who
// declared no policy asked for the deterministic reading, which costs no
// dispatch and needs no authority.
func selectPolicyOf(n *ir.Node) ir.SelectPolicy {
	if n == nil || n.Policy == "" {
		return ir.SelectFirstSuccess
	}
	return n.Policy
}

// selectDispatchesJudge reports whether this node's work is a judge dispatch.
// It is the driver's question — the judge attempt is the node's own — and the
// kind switch answers it in classifyKind.
func selectDispatchesJudge(n *ir.Node) bool {
	return n != nil && n.Kind == ir.KindSelect && selectPolicyOf(n) == ir.SelectJudge
}

// selectCandidatesResolved reports whether the node's candidate set is already
// in its ledger. A judge dispatch waits for it: the verdict names one of the
// candidates, so dispatching a judge against an unresolved set would pay a model
// to choose among nothing.
func (st *NodeState) selectCandidatesResolved() bool {
	return st != nil && len(st.Candidates) > 0
}

// selectStepOnce drives one selection cycle: the first ready select whose
// candidate set can be resolved gets it resolved. A set whose producer has not
// terminated is a WAIT, not an empty list — the same rule fan-out, aggregate,
// and settlement planning follow — so the cycle falls through and the run does
// whatever else it can.
//
// It runs for the judge policy too. Resolving the set first is what keeps an
// unreadable candidate source from costing a model dispatch: the node stays
// ready, records its candidates, and the judge is dispatched by the ordinary
// start path on a later cycle.
func (r *Run) selectStepOnce() (bool, error) {
	for _, p := range r.ready {
		n := r.byPath[p]
		if n == nil || n.Kind != ir.KindSelect {
			continue
		}
		st := r.state[p]
		if st == nil || st.State != StateReady || st.selectCandidatesResolved() {
			continue
		}
		values, deferred, refusal := r.selectCandidates(n)
		if deferred {
			continue
		}
		return true, r.Apply(SelectResolvedEvent{NodePath: p, Candidates: values, Refusal: refusal})
	}
	return false, nil
}

// selectCandidates resolves one select node's candidate set into the values the
// reducer decides over. The three answers are deliberate and mirror fanoutItems:
// a producer that has not terminated DEFERS (a wait), an unreadable or empty
// source is a REFUSAL the reducer turns into the node's terminal state, and
// anything else is the list, in the source's own order.
func (r *Run) selectCandidates(n *ir.Node) (values []any, deferred bool, refusal string) {
	sel := n.Candidates
	if sel == nil || sel.From == nil {
		return nil, false, ReasonSelectCandidatesUnavailable + ": " + n.Path +
			" declares no candidates selector to choose among"
	}
	if p := r.awaitingProducer(sel); p != "" {
		return nil, true, ""
	}
	v, err := r.selectorValue(sel)
	if err != nil {
		return nil, false, ReasonSelectCandidatesUnavailable + ": " + n.Path + " candidates: " + err.Error()
	}
	if sel.AsList {
		// as_list wraps the whole value as ONE candidate: the author asked to
		// choose among a single workstream that happens to be a list, and a
		// predicate over it would be a filter with nothing to filter.
		return []any{v}, false, ""
	}
	elems, why := elementsFromValue(v)
	if why != "" {
		return nil, false, ReasonSelectCandidatesUnavailable + ": " + n.Path + " candidates " + sel.From.String() + " " + why
	}
	selected, why := filterElements(elems, sel.Predicate)
	if why != "" {
		return nil, false, ReasonSelectCandidatesUnavailable + ": " + n.Path + " candidates " + sel.From.String() + " " + why
	}
	if len(selected) == 0 {
		return nil, false, ReasonSelectCandidatesEmpty + ": " + n.Path + " candidates " + sel.From.String() +
			" resolved to nothing; no policy can choose among an empty set"
	}
	return selected, false, ""
}

// onSelectResolved folds one resolved candidate set. A deterministic policy
// decides here — it dispatches nothing, so the choice is a value step inside the
// reducer, like an emit's render — and a judge policy records the set and leaves
// the node ready for its judge attempt.
func (r *Run) onSelectResolved(e SelectResolvedEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: select_resolved for unknown node %q", e.NodePath)
	}
	if st.State != StateReady {
		return fmt.Errorf("workflow/exec: select_resolved for node %s in state %s", e.NodePath, st.State)
	}
	if e.Refusal != "" {
		return r.failTerminal(e.NodePath, e.Refusal)
	}
	n := r.byPath[e.NodePath]
	if n == nil || n.Kind != ir.KindSelect {
		return fmt.Errorf("workflow/exec: select_resolved for node %q, which is not a select", e.NodePath)
	}
	if st.selectCandidatesResolved() {
		return fmt.Errorf("workflow/exec: node %s already resolved its candidate set", e.NodePath)
	}
	policy := selectPolicyOf(n)
	st.SelectPolicy = policy
	st.Candidates = r.classifyCandidates(e.Candidates)
	switch policy {
	case ir.SelectFirstSuccess, ir.SelectSmallestDiff:
		idx, ref, why := chooseBySet(policy, st.Candidates)
		if why != "" {
			return r.failTerminal(e.NodePath, why)
		}
		return r.derive(SelectChosenEvent{NodePath: e.NodePath, Policy: policy, Chosen: idx, Ref: ref})
	case ir.SelectJudge:
		if n.Judge == nil {
			return r.failTerminal(e.NodePath, ReasonSelectJudgeMissing+": "+e.NodePath+
				" declares policy: judge with no judge payload, so nobody is named to decide")
		}
		// The candidate set is the judge's input, and it is recorded before the
		// dispatch so the write-ahead record of the attempt is the only thing a
		// crash can leave outstanding.
		return nil
	}
	return r.failTerminal(e.NodePath, ReasonSelectPolicyUnsupported+": "+e.NodePath+" policy "+
		selectPolicyLabel(policy))
}

// selectPolicyLabel renders a policy for a reason line, so an undeclared one
// reads as the folded default rather than as an empty quote.
func selectPolicyLabel(p ir.SelectPolicy) string {
	if p == "" {
		return "no policy declared; the vocabulary is judge|first_success|smallest_diff"
	}
	return string(p) + "; the vocabulary is judge|first_success|smallest_diff"
}

// onSelectChosen applies one decision. It is the single place a selection
// becomes the node's outcome, so the deterministic policies and the judge's
// verdict cannot disagree about what closing a select node means.
func (r *Run) onSelectChosen(e SelectChosenEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: select_chosen for unknown node %q", e.NodePath)
	}
	if st.State.Terminal() {
		return fmt.Errorf("workflow/exec: select_chosen for node %s, which is already %s", e.NodePath, st.State)
	}
	if e.Chosen < 0 || e.Chosen >= len(st.Candidates) {
		return fmt.Errorf("workflow/exec: select_chosen for %s names candidate %d of %d",
			e.NodePath, e.Chosen, len(st.Candidates))
	}
	n := r.byPath[e.NodePath]
	if n == nil {
		return fmt.Errorf("workflow/exec: select_chosen for node %q that is not in the graph", e.NodePath)
	}
	winner := st.Candidates[e.Chosen]
	text, err := recordedValue(winner.Value)
	if err != nil {
		return r.failTerminal(e.NodePath, ReasonSelectCandidatesUnavailable+": encoding the winning candidate: "+err.Error())
	}
	st.Chosen = e.Chosen
	st.Selection = winner.Value
	st.SelectPolicy = e.Policy
	st.Result = text
	r.recordOutput(st, NodeOutput{
		NodePath: n.Path,
		Kind:     n.Kind,
		Name:     selectBindingName(n),
		Value:    text,
	})
	// The losers' proposals are NOT disposed of here. A select that discarded
	// them would throw away sealed work nobody asked it to touch, so the run's
	// own ledger keeps them open and its close refuses to call the run a success
	// over them. Saying so once is what turns an author's surprise into a fact
	// they can act on.
	if losers := nonWinningProposals(st.Candidates, e.Chosen); len(losers) > 0 {
		r.warnOnce("select-open:"+e.NodePath, fmt.Sprintf(
			"%s: chose candidate %d; %d non-winning candidate proposal(s) stay open (%s) and need their own disposition — the run will not report success over them",
			e.NodePath, e.Chosen, len(losers), strings.Join(losers, ",")))
	}
	return r.derive(NodeSucceededEvent{NodePath: e.NodePath, Outputs: outputNames(st)})
}

// nonWinningProposals lists the proposal ids of every candidate that did not
// win, so the residue the selection leaves standing is named.
func nonWinningProposals(cands []Candidate, chosen int) []string {
	var out []string
	for i, c := range cands {
		if i == chosen {
			continue
		}
		if m := objectOf(c.Value); m != nil {
			if id := handleID(m); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

// classifyCandidates reads every resolved element into the candidate ledger, in
// resolved order.
func (r *Run) classifyCandidates(values []any) []Candidate {
	out := make([]Candidate, 0, len(values))
	for i, v := range values {
		out = append(out, r.classifyCandidate(i, v))
	}
	return out
}

// classifyCandidate reads one candidate's facts. The correlated node — when the
// element names one this run holds — is the strongest status available; failing
// that the element's own fields decide, because a candidate value that survived
// into the collect is a delivered value unless it says otherwise.
func (r *Run) classifyCandidate(i int, v any) Candidate {
	c := Candidate{Index: i, Value: v, DiffLines: -1, State: StateSucceeded}
	m := objectOf(v)
	if m == nil {
		if s, ok := v.(string); ok {
			c.Ref = strings.TrimSpace(s)
			if n := r.nodeRef(c.Ref); n != nil {
				c.Node = n.Path
				if st := r.state[n.Path]; st != nil {
					c.State = st.State
					c.Reason = st.Reason
				}
			}
		}
		return c
	}
	c.Ref = candidateRef(m)
	if path := r.candidateNode(m); path != "" {
		c.Node = path
		if st := r.state[path]; st != nil {
			c.State = st.State
			c.Reason = st.Reason
		}
	} else if failed, _ := m["failed"].(bool); failed {
		c.State = StateFailed
		if why, ok := m["reason"].(string); ok {
			c.Reason = why
		}
	} else if status, ok := m["status"].(string); ok && status == StatusError {
		c.State = StateFailed
	}
	if d := candidateDiff(m); d >= 0 {
		c.DiffLines = d
	}
	return c
}

// candidateRef is the identity a verdict matches: the proposal a handle sealed,
// else the id, node, path, or step the element names.
func candidateRef(m map[string]any) string {
	for _, k := range []string{"proposal_id", "id", "node", "path", "step"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// candidateNode resolves the node a candidate element names, when it names one in
// this run. Only `node:`/`path:` and `step:` are read: `proposal_id` and `id` name
// a proposal, not a node, and correlating them would be a guess.
func (r *Run) candidateNode(m map[string]any) string {
	for _, k := range []string{"node", "path"} {
		if s, ok := m[k].(string); ok {
			if _, held := r.byPath[s]; held {
				return s
			}
		}
	}
	if s, ok := m["step"].(string); ok {
		if n, held := r.byID[s]; held {
			return n.Path
		}
	}
	return ""
}

// nodeRef resolves a bare reference to a node in this run: a node path first,
// then a top-level step id.
func (r *Run) nodeRef(ref string) *ir.Node {
	if ref == "" {
		return nil
	}
	if n, held := r.byPath[ref]; held {
		return n
	}
	if n, held := r.byID[ref]; held {
		return n
	}
	return nil
}

// candidateDiff reads the changed-line count out of one candidate's fields, or -1
// when it carries none. Three spellings are accepted because a delegate's
// delivered payload is the author's shape, not this package's: `changed_lines`,
// `diff_lines`, or a `diff` number/mapping carrying either.
func candidateDiff(m map[string]any) int {
	for _, k := range []string{"changed_lines", "diff_lines"} {
		if n, ok := intValue(m[k]); ok {
			return n
		}
	}
	d, ok := m["diff"]
	if !ok {
		return -1
	}
	if n, ok := intValue(d); ok {
		return n
	}
	if inner := objectOf(d); inner != nil {
		for _, k := range []string{"changed_lines", "diff_lines", "lines"} {
			if n, ok := intValue(inner[k]); ok {
				return n
			}
		}
	}
	return -1
}

// intValue reads a JSON/YAML-decoded number as an int. A decoded document gives
// float64, a Go-built fixture may give any integer width, and a string that
// spells a count is accepted because a lane that delivered one meant the number.
func intValue(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case float32:
		return int(t), true
	case int:
		return t, true
	case int32:
		return int(t), true
	case int64:
		return int(t), true
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n), true
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n, true
		}
	}
	return 0, false
}

// chooseBySet applies one deterministic policy to the candidate ledger. It
// answers the winner's index and ref, or a refusal stating why no candidate
// qualified — never a silent fallback to another policy, because a
// smallest_diff over a set with no diff facts would otherwise look like a
// first_success the author never wrote.
func chooseBySet(policy ir.SelectPolicy, cands []Candidate) (int, string, string) {
	switch policy {
	case ir.SelectFirstSuccess:
		for _, c := range cands {
			if c.State == StateSucceeded {
				return c.Index, c.Ref, ""
			}
		}
		return -1, "", ReasonSelectNoCandidate + ": " + fmt.Sprintf(
			"none of the %d candidate(s) succeeded, and first_success takes the first that did", len(cands))
	case ir.SelectSmallestDiff:
		best := -1
		for i, c := range cands {
			if c.State != StateSucceeded || c.DiffLines < 0 {
				continue
			}
			// Strictly smaller, so a tie keeps the earlier candidate: declared
			// order is the tie-break, and it needs no second rule.
			if best < 0 || c.DiffLines < cands[best].DiffLines {
				best = i
			}
		}
		if best < 0 {
			return -1, "", ReasonSelectNoDiffMetadata + ": " + fmt.Sprintf(
				"no candidate out of %d carries a changed-line count (%s), so smallest_diff has nothing to compare",
				len(cands), "changed_lines or diff_lines")
		}
		return cands[best].Index, cands[best].Ref, ""
	}
	return -1, "", ReasonSelectPolicyUnsupported + ": " + selectPolicyLabel(policy)
}

// verdict is the structured answer a judge delegate delivers. It is parsed, not
// trusted: the judge names a winner and may say why, and the fields here are the
// two facts the reducer needs.
type verdict struct {
	// Winner is the winning candidate's index, and Ref its identity, when the
	// verdict named one instead of a position. Exactly one is meaningful.
	Winner int
	Ref    string
	Reason string
}

// parseVerdict reads a judge's delivered result. A verdict is a JSON object
// naming `winner` (or `pick`) as a candidate index, a numeric string, a candidate
// ref, or a mapping with `index` or `proposal_id`; `reason` is quoted when
// present. Anything else is refused with a reason rather than guessed at: a judge
// whose answer nobody can read has not decided anything.
func parseVerdict(text string) (verdict, string) {
	obj, ok := decodeJSONObject(text)
	if !ok {
		return verdict{}, ReasonSelectVerdictInvalid +
			": the judge's result is not a JSON object, so it names no winner"
	}
	raw, ok := obj["winner"]
	if !ok {
		raw, ok = obj["pick"]
	}
	if !ok {
		return verdict{}, ReasonSelectVerdictInvalid +
			": the judge's verdict carries no winner field (winner, or pick)"
	}
	out := verdict{}
	if why, ok := obj["reason"].(string); ok {
		out.Reason = why
	}
	switch t := raw.(type) {
	case map[string]any:
		if n, ok := intValue(t["index"]); ok {
			out.Winner = n
			return out, ""
		}
		if s, ok := t["proposal_id"].(string); ok && strings.TrimSpace(s) != "" {
			out.Ref = strings.TrimSpace(s)
			return out, ""
		}
	case string:
		if s := strings.TrimSpace(t); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				out.Winner = n
				return out, ""
			}
			out.Ref = s
			return out, ""
		}
	default:
		if n, ok := intValue(raw); ok {
			out.Winner = n
			return out, ""
		}
	}
	return verdict{}, ReasonSelectVerdictInvalid +
		": the judge's verdict names no candidate it could be matched against"
}

// matchVerdict resolves a parsed verdict onto one candidate. A verdict that picks
// nobody in the set is refused and the candidate set is named, so a judge that
// answered about a proposal it was not offered is visible instead of silently
// selecting candidate zero.
func matchVerdict(cands []Candidate, v verdict) (int, string) {
	if v.Ref != "" {
		for _, c := range cands {
			if c.Ref != "" && c.Ref == v.Ref {
				return c.Index, ""
			}
		}
		for _, c := range cands {
			if c.Node != "" && c.Node == v.Ref {
				return c.Index, ""
			}
		}
		return -1, ReasonSelectVerdictUnmatched + ": the judge named " + strconv.Quote(v.Ref) +
			", which is not one of the " + strconv.Itoa(len(cands)) + " candidate(s)"
	}
	if v.Winner < 0 || v.Winner >= len(cands) {
		return -1, ReasonSelectVerdictUnmatched + ": the judge named candidate index " +
			strconv.Itoa(v.Winner) + ", out of range for " + strconv.Itoa(len(cands)) + " candidate(s)"
	}
	return v.Winner, ""
}

// foldVerdict turns a completed judge attempt into the node's decision. The
// judge's delivered result is recorded verbatim on the node first, so a trace can
// quote what decided as well as what was decided.
func (r *Run) foldVerdict(n *ir.Node, st *NodeState) error {
	st.Verdict = st.Result
	v, why := parseVerdict(st.Result)
	if why != "" {
		return r.failTerminal(n.Path, why)
	}
	if !st.selectCandidatesResolved() {
		return r.failTerminal(n.Path, ReasonSelectCandidatesUnavailable+": "+n.Path+
			" ran its judge with no resolved candidate set to decide among")
	}
	idx, why := matchVerdict(st.Candidates, v)
	if why != "" {
		return r.failTerminal(n.Path, why)
	}
	return r.derive(SelectChosenEvent{
		NodePath: n.Path,
		Policy:   ir.SelectJudge,
		Chosen:   idx,
		Ref:      st.Candidates[idx].Ref,
		Reason:   v.Reason,
	})
}

// selectionValue is a select node's recorded value: the winning candidate plus
// the decision around it. `winner` is the key a downstream settle reads
// (`steps.<pick>.output.winner`), and a winning handle's proposal id is repeated
// at the top level so `steps.<pick>.output` alone is a usable proposal ref.
func (st *NodeState) selectionValue() map[string]any {
	out := map[string]any{
		"winner": st.Selection,
		"chosen": st.Chosen,
		"policy": string(st.SelectPolicy),
		"count":  len(st.Candidates),
	}
	if st.Chosen >= 0 && st.Chosen < len(st.Candidates) {
		if ref := st.Candidates[st.Chosen].Ref; ref != "" {
			out["ref"] = ref
		}
	}
	if m := objectOf(st.Selection); m != nil {
		if id := handleID(m); id != "" {
			out["proposal_id"] = id
		}
	}
	if st.Verdict != "" {
		out["verdict"] = st.Verdict
	}
	return out
}

// selectBindingName is the name a select's value is bound under: its declared
// output name, else its step id, else its path — the rule emit, settle, fan-out,
// and aggregate follow, so `steps.<id>.output` and a named binding read one value.
func selectBindingName(n *ir.Node) string {
	if n.Output != nil && n.Output.Name != "" {
		return n.Output.Name
	}
	if n.ID != "" {
		return n.ID
	}
	return n.Path
}
