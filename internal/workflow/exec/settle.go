package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Settlement (yaah-0u0v.13). A settle node is an ADAPTER over the proposal
// runtime, not a second settlement implementation: the scheduler resolves
// which proposals it owns from recorded run state, asks the door to dispose of
// them one at a time, and records what came back. Nothing in this file merges
// a tree, names a worktree, or knows what an apply strategy is — those are
// internal/dispatch's, reached through Runtime.Settle by the host door.
//
// The split follows the package's one rule: the driver calls the door and the
// reducer owns the state. So handle resolution is a read of run state, the
// plan and every disposition arrive as INPUT events (settlement_planned,
// proposal_settled), and the decisions they carry — the linearity guard,
// on_conflict, on_empty, the node's disposition — are made in Apply.

// Reasons and statuses the settlement family states.
const (
	// ReasonProposalAlreadySettled is the runtime linearity guard: a proposal
	// has exactly one disposition, so a settle naming an id this run already
	// disposed of is refused before any settlement is asked for.
	ReasonProposalAlreadySettled = "proposal_already_settled"
	// ReasonProposalSettleInFlight is the same guard seen from the other side:
	// another settle node holds the proposal right now.
	ReasonProposalSettleInFlight = "proposal_settle_in_flight"
	// ReasonOpenProposalsAtExit is the run-level half of the guard: a run must
	// not report success while a proposal it sealed has no disposition. The
	// v1 vocabulary declares no on_abort field for that residue (WF025 says so
	// at its own rule), so the escape is the caller's Config.OpenProposals.
	ReasonOpenProposalsAtExit = "open_proposals_at_run_end"
	// ReasonSettlementFailed prefixes a door malfunction or a settlement whose
	// status came back neither settled nor conflict.
	ReasonSettlementFailed = "settlement_failed"
	// ReasonSettleActionUnsupported names a settle action outside the four
	// settlement verbs. continue and fork are NOT settlement verbs — they are
	// re-run bindings on a delegate (continue_from / fork_from) — so they are
	// refused here rather than mapped.
	ReasonSettleActionUnsupported = "unsupported_settle_action"
	// ReasonSettleSourceUnavailable prefixes a proposals or where handle the
	// scheduler cannot resolve from recorded run state: an output carrying no
	// proposal handle, an item binding inside a container body, or a filter
	// field no handle has.
	ReasonSettleSourceUnavailable = "settle_source_unavailable"
	// ReasonNoProposalsMatched is on_empty: fail — matching nothing was an
	// outcome the author declared unacceptable.
	ReasonNoProposalsMatched = "no_proposals_matched"
)

// Settlement statuses, verbatim from the proposal runtime's one settlement
// envelope. The scheduler never synthesizes one.
const (
	SettlementStatusSettled  = "settled"
	SettlementStatusConflict = "conflict"
	SettlementStatusError    = "error"
)

// ProposalHandle is the typed record of a proposal an attempt sealed. The
// envelope's proposal_id is the whole fact; the rest is what a settle node and
// a later trace need in order to name where it came from without re-deriving
// it from per-lane plumbing.
type ProposalHandle struct {
	ProposalID string `json:"proposal_id"`
	// Producer is the node path that sealed it.
	Producer   string `json:"producer,omitempty"`
	Role       string `json:"role,omitempty"`
	Lane       string `json:"lane,omitempty"`
	DispatchID string `json:"dispatch_id,omitempty"`
}

// ProposalRef is one element of a settle node's resolved handle set.
type ProposalRef struct {
	ProposalID string `json:"proposal_id"`
	Producer   string `json:"producer,omitempty"`
}

// SettlePlan is one settle node's resolved work: the handles to dispose of, in
// the order the door is asked to take them, plus the dispositions the author
// declared for what the door reports back.
type SettlePlan struct {
	Refs       []ProposalRef     `json:"refs"`
	Action     ir.SettleAction   `json:"action"`
	Policy     ir.SettlePolicy   `json:"policy,omitempty"`
	Order      ir.SettleOrder    `json:"order,omitempty"`
	OnConflict ir.ConflictPolicy `json:"on_conflict,omitempty"`
	Empty      ir.EmptyPolicy    `json:"on_empty,omitempty"`
}

// Settled is the scheduler's view of one settlement. It mirrors the proposal
// runtime's settlement envelope the way Completion mirrors the dispatch
// envelope — same facts, separate type, because this package may not import
// internal/dispatch. internal/workflow/host is the producer that bridges them.
type Settled struct {
	ProposalID string `json:"proposal_id"`
	Producer   string `json:"producer,omitempty"`
	// Action is the verb the door was asked to apply.
	Action string `json:"action,omitempty"`
	// Status is settled, conflict, or error — verbatim.
	Status string `json:"status,omitempty"`
	// Outcome is the runtime's typed work/scope/record/notes block. It is
	// multi-line prose and therefore never reaches an event log line.
	Outcome string `json:"outcome,omitempty"`
	Error   string `json:"error,omitempty"`
	// Conflicts are the overlap paths a refused apply named.
	Conflicts []string `json:"conflicts,omitempty"`
	// Files are the paths the settlement touched, when the envelope reported.
	Files []string `json:"files,omitempty"`
	// LeftOpen reports that the proposal has no disposition now: a conflict
	// leaves the worktree standing, and an error leaves everything standing.
	LeftOpen bool `json:"left_open,omitempty"`
}

// settlementVerb maps a resolved settle action onto the door's verb. The
// vocabulary is exactly four and the default arm refuses everything else —
// which is where continue and fork are turned away.
func settlementVerb(a ir.SettleAction) (string, bool) {
	switch a {
	case ir.SettleSelect:
		return "select", true
	case ir.SettleApply:
		return "apply", true
	case ir.SettleRelease:
		return "release", true
	case ir.SettleDiscard:
		return "discard", true
	}
	return "", false
}

// nextSettlePath returns the first ready settle node the driver may work on.
// The ready queue is kept in canonical path order, so a graph with two settle
// nodes plans and settles them in the same order every run.
func (r *Run) nextSettlePath() (string, bool) {
	for _, p := range r.ready {
		n := r.byPath[p]
		if n == nil || n.Kind != ir.KindSettle {
			continue
		}
		st := r.state[p]
		if st.State != StateReady {
			continue
		}
		if st.SettlePlan == nil || st.Settling < len(st.SettlePlan.Refs) {
			return p, true
		}
	}
	return "", false
}

// settleStep performs one settlement — or one plan report — for one ready
// settle node, and submits the resulting event. It mutates nothing itself:
// both the plan and each disposition are decided inside Apply.
func (r *Run) settleStep(ctx context.Context, path string) (bool, error) {
	n := r.byPath[path]
	st := r.state[path]
	if n == nil || st == nil {
		return false, nil
	}
	if st.SettlePlan == nil {
		refs, deferred, refusal := r.resolveHandles(n)
		if deferred {
			return false, nil
		}
		return true, r.Apply(SettlementPlannedEvent{
			NodePath:   path,
			Refs:       refs,
			Action:     n.Action,
			Policy:     n.SettlePolicy,
			Order:      n.Order,
			OnConflict: n.OnConflict,
			Empty:      n.OnEmpty,
			Refusal:    refusal,
		})
	}
	if st.Settling >= len(st.SettlePlan.Refs) {
		return false, nil
	}
	ref := st.SettlePlan.Refs[st.Settling]
	verb, ok := settlementVerb(st.SettlePlan.Action)
	if !ok {
		return true, r.Apply(ProposalSettledEvent{
			NodePath: path,
			Settled:  Settled{ProposalID: ref.ProposalID, Producer: ref.Producer, Status: SettlementStatusError, Action: string(st.SettlePlan.Action), LeftOpen: true},
			// An unmappable action is a door-side refusal, not a
			// settlement the lane reported: no proposal was touched.
			Refused: ReasonSettleActionUnsupported + ": " + string(st.SettlePlan.Action) +
				"; the settlement vocabulary is select|apply|release|discard",
		})
	}
	// The intent is recorded — and, with a Store configured, persisted — before
	// the door is asked, so a crash between the two leaves "asked, outcome
	// unknown" on the record instead of a silent gap that a resume would fill by
	// asking a second time (yaah-0u0v.16).
	if err := r.Apply(SettlementAskedEvent{NodePath: path, ProposalID: ref.ProposalID, Action: verb}); err != nil {
		return true, err
	}
	s, err := r.rt.Settle(ctx, SettleRequest{
		NodePath:   path,
		ProposalID: ref.ProposalID,
		Producer:   ref.Producer,
		Verb:       verb,
		Policy:     st.SettlePlan.Policy,
	})
	if s.ProposalID == "" {
		s.ProposalID = ref.ProposalID
	}
	if s.Producer == "" {
		s.Producer = ref.Producer
	}
	s.Action = verb
	ev := ProposalSettledEvent{NodePath: path, Settled: s}
	if err != nil {
		// A door malfunction is reported as an error disposition: there is no
		// envelope behind it, so the scheduler asserts nothing about what the
		// world did and leaves the proposal open.
		ev.DoorError = err.Error()
		if s.Status == "" {
			s.Status = SettlementStatusError
		}
		ev.Settled = s
	}
	return true, r.Apply(ev)
}

// onSettlementPlanned records a settle node's resolved handle set and applies
// the two rules that decide whether the node may work at all: on_empty on an
// empty set, and the linearity guard on a proposal this run already owns.
//
// The guard is checked before the first settlement is asked for, so a node
// that names one already-disposed proposal settles nothing — not even the
// other elements of its own set.
func (r *Run) onSettlementPlanned(e SettlementPlannedEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: settlement_planned for unknown node %q", e.NodePath)
	}
	if st.State != StateReady {
		return fmt.Errorf("workflow/exec: settlement_planned for node %s in state %s", e.NodePath, st.State)
	}
	n := r.byPath[e.NodePath]
	if e.Refusal != "" {
		return r.failTerminal(e.NodePath, e.Refusal)
	}
	if _, ok := settlementVerb(e.Action); !ok {
		return r.failTerminal(e.NodePath, ReasonSettleActionUnsupported+": "+string(e.Action)+
			"; the settlement vocabulary is select|apply|release|discard")
	}
	if len(e.Refs) == 0 {
		st.SettlePlan = &SettlePlan{Action: e.Action, Policy: e.Policy, Order: e.Order, OnConflict: e.OnConflict, Empty: e.Empty}
		switch e.Empty {
		case ir.OnEmptyFail:
			return r.failTerminal(e.NodePath, ReasonNoProposalsMatched+": "+e.NodePath+
				" resolved no proposals and declares on_empty: fail")
		case ir.OnEmptySkip:
			return r.derive(NodeSkippedEvent{
				NodePath: e.NodePath,
				Reason:   ReasonNoProposalsMatched + ": " + e.NodePath + " resolved no proposals (on_empty: skip)",
			})
		default:
			// pass is the declared answer and the folded default: nothing to
			// dispose of is a completed settle, and the run says so through
			// the node's recorded value rather than through a warning.
			return r.closeSettlement(n, st)
		}
	}
	for _, ref := range e.Refs {
		if by, done := r.settled[ref.ProposalID]; done {
			return r.failTerminal(e.NodePath, ReasonProposalAlreadySettled+": proposal "+ref.ProposalID+
				" is already disposed of by "+by+"; a proposal has exactly one disposition")
		}
		if by, held := r.claims[ref.ProposalID]; held && by != e.NodePath {
			return r.failTerminal(e.NodePath, ReasonProposalSettleInFlight+": proposal "+ref.ProposalID+
				" is being settled by "+by)
		}
	}
	plan := &SettlePlan{
		Refs:       append([]ProposalRef(nil), e.Refs...),
		Action:     e.Action,
		Policy:     e.Policy,
		Order:      e.Order,
		OnConflict: e.OnConflict,
		Empty:      e.Empty,
	}
	st.SettlePlan = plan
	st.Settling = 0
	st.Settled = nil
	for _, ref := range plan.Refs {
		r.claims[ref.ProposalID] = e.NodePath
	}
	return nil
}

// onSettlementAsked records the disposition the run is about to ask the door
// for. It is the settlement write-ahead record (yaah-0u0v.16): the driver emits
// this BEFORE Runtime.Settle, so the checkpoint says "asked" before the world
// effect can happen, and a resumed run can tell an unknown outcome from a
// settlement that never started.
func (r *Run) onSettlementAsked(e SettlementAskedEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: settlement_asked for unknown node %q", e.NodePath)
	}
	if st.SettlePlan == nil {
		// A join:any|quorum container disposes of the proposals its NON-winning
		// items sealed, through this same door and the discard verb
		// (yaah-0u0v.19). The check precedes the state guard because that
		// container is TERMINAL by design: the join was decided before the
		// residue was known. It keeps the same write-ahead record a settle node
		// does, so a resume can tell "asked, outcome unknown" from "never asked".
		if n := r.byPath[e.NodePath]; n != nil && n.Kind == ir.KindFanout && cancelJoin(n) {
			return r.onJoinDiscardAsked(e.NodePath, st, e)
		}
		return fmt.Errorf("workflow/exec: settlement_asked for node %s with no planned settlement", e.NodePath)
	}
	if st.State != StateReady {
		return fmt.Errorf("workflow/exec: settlement_asked for node %s in state %s", e.NodePath, st.State)
	}
	if st.Asked != "" {
		return fmt.Errorf("workflow/exec: settlement_asked for node %s while proposal %s is still unanswered",
			e.NodePath, st.Asked)
	}
	if e.ProposalID == "" {
		return fmt.Errorf("workflow/exec: settlement_asked for node %s with no proposal id", e.NodePath)
	}
	st.Asked = e.ProposalID
	return nil
}

// onProposalSettled folds one disposition into the node's record and decides
// what it means for the node: an error ends it, a conflict ends it unless the
// author declared skip, and the last element of the plan closes it succeeded.
func (r *Run) onProposalSettled(e ProposalSettledEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: proposal_settled for unknown node %q", e.NodePath)
	}
	if st.SettlePlan == nil {
		if n := r.byPath[e.NodePath]; n != nil && n.Kind == ir.KindFanout && cancelJoin(n) {
			return r.onJoinDiscardSettled(e.NodePath, st, e)
		}
		return fmt.Errorf("workflow/exec: proposal_settled for node %s with no planned settlement", e.NodePath)
	}
	if st.State != StateReady {
		return fmt.Errorf("workflow/exec: proposal_settled for node %s in state %s", e.NodePath, st.State)
	}
	// The unanswered ask is discharged by any answer, including a refusal: what
	// Asked records is "a disposition is in flight", and after this event none
	// is.
	st.Asked = ""
	if e.DoorError != "" {
		st.SettlePlan = nil
		return r.failTerminal(e.NodePath, ReasonSettlementFailed+": "+e.NodePath+": "+e.DoorError)
	}
	s := e.Settled
	if e.Refused != "" {
		st.SettlePlan = nil
		return r.failTerminal(e.NodePath, e.Refused)
	}
	st.Settled = append(st.Settled, s)
	st.Settling++
	// Disposition bookkeeping. A settled proposal leaves the open set and
	// takes a permanent claim; a conflict or an error leaves it open and
	// releases the claim, which is what lets the downstream catch settle
	// WF024 demands for on_conflict: skip.
	delete(r.claims, s.ProposalID)
	switch {
	case s.Status == SettlementStatusSettled && !s.LeftOpen:
		r.settled[s.ProposalID] = e.NodePath
		delete(r.open, s.ProposalID)
	default:
		if _, seen := r.open[s.ProposalID]; !seen {
			r.open[s.ProposalID] = s.Producer
		}
	}
	n := r.byPath[e.NodePath]
	switch {
	case s.Status == SettlementStatusError:
		return r.failTerminal(e.NodePath, ReasonSettlementFailed+": proposal "+s.ProposalID+
			" action "+s.Action+" status=error error="+flatten(s.Error))
	case s.Status == SettlementStatusConflict && n != nil && n.OnConflict != ir.OnConflictSkip:
		reason := ReasonSettlementFailed + ": conflict on proposal " + s.ProposalID +
			" action " + s.Action + " paths=" + orDash(strings.Join(s.Conflicts, ","))
		if n.OnConflict == ir.OnConflictEscalate {
			// The escalation target is a human response, and a human response
			// is an approval door the scheduler core does not have. The
			// conflict is surfaced as the node's failure reason, naming what
			// is missing rather than pretending it was answered.
			reason += " (on_conflict: escalate needs a human response door, which is not the scheduler core)"
		}
		return r.failTerminal(e.NodePath, reason)
	}
	if st.Settling < len(st.SettlePlan.Refs) {
		return nil
	}
	return r.closeSettlement(n, st)
}

// closeSettlement records the node's settled value and closes it. The value is
// the structured record a later emit reads through `steps.<id>.output.settled`,
// and the same record the run's output list carries, so the two can never
// disagree.
func (r *Run) closeSettlement(n *ir.Node, st *NodeState) error {
	encoded, err := json.Marshal(st.settledValue())
	if err != nil {
		return r.failTerminal(st.Path, ReasonSettlementFailed+": encoding the settled value: "+err.Error())
	}
	st.Result = string(encoded)
	if n != nil {
		r.recordOutput(st, NodeOutput{
			NodePath: n.Path,
			Kind:     n.Kind,
			Name:     settleBindingName(n),
			Value:    st.Result,
			Contract: st.Contract,
		})
	}
	return r.derive(NodeSucceededEvent{NodePath: st.Path, Outputs: outputNames(st)})
}

// settleBindingName is the name a settle's value is bound under: its declared
// output name, else its step id, else its path — the same rule emit follows,
// so `steps.<id>.output` and the named binding read one value.
func settleBindingName(n *ir.Node) string {
	if n.Output != nil && n.Output.Name != "" {
		return n.Output.Name
	}
	if n.ID != "" {
		return n.ID
	}
	return n.Path
}

// settledValue is the structured value a settled node reports: the counts an
// author interpolates into a report, plus the per-proposal record a trace
// reads. Maps are built from ordered slices and encoded by encoding/json, so
// the text is a function of the dispositions and nothing else.
func (st *NodeState) settledValue() map[string]any {
	var settled, conflicted, leftOpen int
	ids := make([]string, 0, len(st.Settled))
	results := make([]any, 0, len(st.Settled))
	for _, s := range st.Settled {
		m := map[string]any{
			"proposal_id": s.ProposalID,
			"action":      s.Action,
			"status":      s.Status,
		}
		if s.Producer != "" {
			m["producer"] = s.Producer
		}
		if len(s.Conflicts) > 0 {
			m["conflicts"] = append([]string(nil), s.Conflicts...)
			conflicted++
		}
		if len(s.Files) > 0 {
			m["files"] = append([]string(nil), s.Files...)
		}
		if s.Error != "" {
			m["error"] = s.Error
		}
		if s.Outcome != "" {
			m["outcome"] = s.Outcome
		}
		switch {
		case s.LeftOpen || s.Status != SettlementStatusSettled:
			leftOpen++
		default:
			settled++
			ids = append(ids, s.ProposalID)
		}
		results = append(results, m)
	}
	return map[string]any{
		"settled":    settled,
		"conflicted": conflicted,
		"left_open":  leftOpen,
		"ids":        ids,
		"results":    results,
	}
}

// openProposals lists the proposal ids this run holds without a disposition,
// sorted, so the reason a run refuses to end succeeded names the same list
// every time.
func (r *Run) openProposals() []string {
	out := make([]string, 0, len(r.open))
	for id := range r.open {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// openProposalDetail renders the residue for the run's reason line.
func (r *Run) openProposalDetail() string {
	ids := r.openProposals()
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if by := r.open[id]; by != "" {
			parts = append(parts, id+" ("+by+")")
			continue
		}
		parts = append(parts, id)
	}
	return strings.Join(parts, ", ")
}

// releaseClaims drops the claims a node holds on proposals it will not settle
// after all. It runs when a settle node terminates, so a node that failed
// halfway through its plan leaves the rest of its set settle-able instead of
// walled off, and it never touches a proposal already disposed of.
func (r *Run) releaseClaims(path string) {
	st := r.state[path]
	if st == nil || st.SettlePlan == nil {
		return
	}
	for i := st.Settling; i < len(st.SettlePlan.Refs); i++ {
		id := st.SettlePlan.Refs[i].ProposalID
		if _, done := r.settled[id]; done {
			continue
		}
		if holder, ok := r.claims[id]; ok && holder == path {
			delete(r.claims, id)
		}
	}
}

// resolveHandles turns a settle node's proposals and where selectors into the
// refs it disposes of, and answers three things at once: whether the node may
// be planned yet (a producer that has not terminated is a wait, not an empty
// set), what the set is, and whether the declaration is something this bead
// can execute at all.
func (r *Run) resolveHandles(n *ir.Node) (refs []ProposalRef, deferred bool, refusal string) {
	if n.Proposals == nil || n.Proposals.From == nil {
		return nil, false, ReasonSettleSourceUnavailable + ": node declares no proposals handle set"
	}
	for _, sel := range []*ir.Selector{n.Proposals, n.Where} {
		if sel == nil || sel.From == nil {
			continue
		}
		if p := r.awaitingProducer(sel); p != "" {
			return nil, true, ""
		}
	}
	refs, refusal = r.handleRefs(n.Proposals)
	if refusal != "" {
		return nil, false, refusal
	}
	if n.Where != nil && n.Where.From != nil {
		if !sameSource(n.Proposals.From, n.Where.From) {
			return nil, false, ReasonSettleSourceUnavailable + ": where reads " + n.Where.From.String() +
				" while proposals reads " + n.Proposals.From.String() +
				"; correlating two steps' sets needs the join across two collects, which the scheduler does not perform yet"
		}
		filtered, why := applyFilter(refs, n.Where)
		if why != "" {
			return nil, false, why
		}
		refs = filtered
	}
	sortRefs(refs, n.Order)
	if n.Order == ir.OrderSmallestDiffFirst && len(refs) > 1 {
		r.warnOnce("settle-order:"+n.Path, fmt.Sprintf(
			"%s: order: smallest_diff_first settled in canonical proposal order — the scheduler holds no diff-size fact to order on", n.Path))
	}
	return refs, false, ""
}

// awaitingProducer returns the path of a step the selector reads that has not
// terminated yet, or "" when there is nothing to wait for.
func (r *Run) awaitingProducer(sel *ir.Selector) string {
	if sel == nil || sel.From == nil {
		return ""
	}
	if sel.From.Kind != ir.SourceStepOutput && sel.From.Kind != ir.SourceStepCollect {
		return ""
	}
	dep, ok := r.byID[sel.From.StepID]
	if !ok {
		return ""
	}
	if r.state[dep.Path].State.Terminal() {
		return ""
	}
	return dep.Path
}

// handleRefs resolves a proposals selector into proposal refs.
func (r *Run) handleRefs(sel *ir.Selector) ([]ProposalRef, string) {
	src := sel.From
	if src.Kind == ir.SourceItem {
		return nil, ReasonSettleSourceUnavailable + ": " + src.String() +
			" is an item binding inside a container body; a settle that reads its own item's scope is not in the scheduler core yet"
	}
	v, err := r.selectorValue(sel)
	if err != nil {
		return nil, ReasonSettleSourceUnavailable + ": " + err.Error()
	}
	if sel.AsList {
		v = []any{v}
	}
	refs, why := refsFromValue(v)
	if why != "" {
		return nil, ReasonSettleSourceUnavailable + ": " + src.String() + " " + why
	}
	// A set read from a step — its own output or a fan-out's collected items —
	// names that step as the producer of any element that did not record one.
	// A fan-out collect is the case this matters for: its elements are the
	// items' recorded values, and the container is the node the run holds
	// responsible for them.
	if src.Kind == ir.SourceStepOutput || src.Kind == ir.SourceStepCollect {
		if dep, ok := r.byID[src.StepID]; ok {
			for i := range refs {
				if refs[i].Producer == "" {
					refs[i].Producer = dep.Path
				}
			}
		}
	}
	// One id settled twice inside one plan is the same bug as two nodes
	// settling it, and the author wrote it in one expression.
	return dedupeRefs(refs), ""
}

// selectorValue walks a compiled selector source against recorded run state.
// Steps read through the same value extraction a template reads, so
// `proposals: {from: steps.work.output.proposal_id}` and the template
// `{{ steps.work.output.proposal_id }}` answer from one place — and a fan-out's
// `steps.<id>.collect` is the joined list that same template would render.
func (r *Run) selectorValue(sel *ir.Selector) (any, error) {
	src := sel.From
	var (
		v   any
		err error
	)
	switch src.Kind {
	case ir.SourceStepOutput:
		dep, ok := r.byID[src.StepID]
		if !ok {
			return nil, fmt.Errorf("selector %s names an out-of-scope step %q", src.String(), src.StepID)
		}
		v = r.stepValue(dep, r.state[dep.Path])
	case ir.SourceStepCollect:
		dep, ok := r.byID[src.StepID]
		if !ok {
			return nil, fmt.Errorf("selector %s names an out-of-scope step %q", src.String(), src.StepID)
		}
		st := r.state[dep.Path]
		if st == nil || st.State != StateSucceeded || st.Collect == nil {
			return nil, fmt.Errorf("selector %s: %s has produced no collected items", src.String(), dep.Path)
		}
		v = st.Collect
	case ir.SourceParams:
		p, ok := r.params[src.Name]
		if !ok {
			return nil, fmt.Errorf("selector %s names an unbound param %q", src.String(), src.Name)
		}
		v = p
	default:
		return nil, fmt.Errorf("selector %s is not resolvable here", src.String())
	}
	v, err = walkValue(v, src.Path)
	return v, err
}

// stepValue is the recorded value of one node — the fenced per-binding value
// extraction (yaah-0u0v.13). A report delegate's value is its delivered text,
// unchanged. A proposal-lane delegate's value is a mapping that carries the
// typed handle, so `steps.<id>.output.proposal_id` resolves and a whole-output
// read renders the handle rather than pretending a proposal is a report. A
// settle's value is its settled record.
//
// A node that never succeeded has no value: callers get the empty result text
// and the reference that asked for it fails as it always has.
func (r *Run) stepValue(n *ir.Node, st *NodeState) any {
	if n == nil || st == nil {
		return ""
	}
	switch n.Kind {
	case ir.KindSettle:
		return st.settledValue()
	case ir.KindFanout:
		// A fan-out's value IS its collected items: `steps.<id>.output` and
		// `steps.<id>.collect` are two readings of one list, in input order.
		return st.collectValue()
	case ir.KindLoop:
		// A loop's value is its accumulation, for the same reason and in the same
		// order: one entry per succeeded iteration.
		return st.collectValue()
	case ir.KindAggregate:
		// An aggregate's value is the combine it performed: the concatenated list,
		// the merged mapping, or a summarize body's product — structured, so a
		// selector can read into it instead of re-parsing its text.
		return st.Combined
	case ir.KindGate:
		// A gate's value is its disposition plus the reason and verdict around it,
		// so `steps.<id>.output.disposition` is what a downstream node routes on
		// (yaah-0u0v.21).
		return st.gateValue()
	case ir.KindSelect:
		// A select's value is its decision: the winning candidate plus the ledger
		// around it, so `steps.<id>.output.winner` is what a downstream settle
		// applies and `steps.<id>.output` alone still names the proposal
		// (yaah-0u0v.20).
		return st.selectionValue()
	case ir.KindDelegate:
		if st.Proposal != nil && st.Proposal.ProposalID != "" {
			m := map[string]any{
				"proposal_id": st.Proposal.ProposalID,
				"text":        st.Result,
				"status":      st.Status,
			}
			if st.Proposal.Role != "" {
				m["role"] = st.Proposal.Role
			}
			if st.Proposal.Lane != "" {
				m["lane"] = st.Proposal.Lane
			}
			if st.World != "" {
				m["world"] = st.World
			}
			if st.StopReason != "" {
				m["stop_reason"] = st.StopReason
			}
			// Per-binding values inside the delivered payload: a structured
			// Report or Verdict body is walked by name, so `output.diff` and
			// `output.verdict` read the same JSON the lane delivered.
			if obj, ok := decodeJSONObject(st.Result); ok {
				for k, val := range obj {
					if _, exists := m[k]; !exists {
						m[k] = val
					}
				}
			}
			return m
		}
	}
	return st.Result
}

// walkValue walks a dotted access path into a recorded value, decoding a JSON
// object body on demand. It is the Scope-side half of the same rule
// ir.template's walkPath applies to a rendered reference.
func walkValue(v any, path []string) (any, error) {
	for _, seg := range path {
		m, ok := v.(map[string]any)
		if !ok {
			if s, isStr := v.(string); isStr {
				if decoded, isObj := decodeJSONObject(s); isObj {
					m = decoded
				}
			}
			if m == nil {
				return nil, fmt.Errorf("value has no field %q (value is not a mapping)", seg)
			}
		}
		next, ok := m[seg]
		if !ok {
			return nil, fmt.Errorf("value has no field %q", seg)
		}
		v = next
	}
	return v, nil
}

func decodeJSONObject(s string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || trimmed[0] != '{' {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return nil, false
	}
	return m, true
}

// refsFromValue turns a resolved selector value into proposal refs. A ref is a
// proposal id spelled either as the string itself or as the proposal_id (or
// id) field of a handle mapping — the two shapes the proposal runtime and the
// value family both use.
func refsFromValue(v any) ([]ProposalRef, string) {
	switch t := v.(type) {
	case nil:
		return nil, "is empty"
	case []any:
		var out []ProposalRef
		for _, e := range t {
			refs, why := refsFromValue(e)
			if why != "" {
				return nil, why
			}
			out = append(out, refs...)
		}
		return out, ""
	case []string:
		out := make([]ProposalRef, 0, len(t))
		for _, s := range t {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, ProposalRef{ProposalID: s})
			}
		}
		if len(out) == 0 {
			return nil, "is empty"
		}
		return out, ""
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []ProposalRef{{ProposalID: s}}, ""
		}
		return nil, "is empty"
	case map[string]any:
		if id := handleID(t); id != "" {
			ref := ProposalRef{ProposalID: id}
			if p, ok := t["producer"].(string); ok {
				ref.Producer = p
			}
			return []ProposalRef{ref}, ""
		}
		if list, ok := t["proposals"]; ok {
			return refsFromValue(list)
		}
	}
	return nil, fmt.Sprintf("is not a proposal handle (got %T)", v)
}

func handleID(m map[string]any) string {
	for _, k := range []string{"proposal_id", "id"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// applyFilter evaluates the settle's where predicate against each resolved
// element. Elements are proposal handles, so the fields a filter can read are
// the handle's own. A field no handle carries is refused rather than compared
// as absent — a filter that matched nothing because a name was mistyped is
// exactly the quiet outcome on_empty exists to prevent.
func applyFilter(refs []ProposalRef, sel *ir.Selector) ([]ProposalRef, string) {
	if sel.Predicate.Op == ir.PredNone && sel.Predicate.Field == "" {
		return refs, ""
	}
	var out []ProposalRef
	for _, ref := range refs {
		v, why := handleField(ref, sel.Predicate.Field)
		if why != "" {
			return nil, why
		}
		ok, err := predicateMatch(v, sel.Predicate)
		if err != nil {
			return nil, ReasonSettleSourceUnavailable + ": where " + err.Error()
		}
		if ok {
			out = append(out, ref)
		}
	}
	return out, ""
}

func handleField(ref ProposalRef, field string) (any, string) {
	switch field {
	case "", "proposal_id", "id":
		return ref.ProposalID, ""
	case "producer", "step":
		if ref.Producer == "" {
			return nil, ReasonSettleSourceUnavailable + ": handle " + ref.ProposalID +
				" records no producing step, so where cannot filter on " + field
		}
		return ref.Producer, ""
	}
	return nil, ReasonSettleSourceUnavailable + fmt.Sprintf(
		": a proposal handle carries no field %q; selecting a proposal by another step's verdict needs the join across two collects, which the scheduler does not perform yet", field)
}

func predicateMatch(v any, p ir.Predicate) (bool, error) {
	switch p.Op {
	case ir.PredNone:
		// A `field:` with no operator is the retention filter: keep the
		// elements whose field is set.
		return !valueEmpty(v), nil
	case ir.PredEquals:
		return valuesEqual(v, p.Arg), nil
	case ir.PredNotEquals:
		return !valuesEqual(v, p.Arg), nil
	case ir.PredIn:
		list, ok := p.Arg.([]any)
		if !ok {
			return false, fmt.Errorf("predicate in requires a list of literals")
		}
		for _, e := range list {
			if valuesEqual(v, e) {
				return true, nil
			}
		}
		return false, nil
	case ir.PredNotEmpty:
		return !valueEmpty(v) == wantTrue(p.Arg), nil
	case ir.PredExists:
		return (v != nil) == wantTrue(p.Arg), nil
	}
	return false, fmt.Errorf("unknown predicate %q", p.Op)
}

func wantTrue(arg any) bool {
	b, _ := arg.(bool)
	return b
}

func valueEmpty(v any) bool {
	return v == nil || v == ""
}

func valuesEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == b
	}
	return scalarText(a) == scalarText(b)
}

func scalarText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return fmt.Sprintf("%t", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func sameSource(a, b *ir.Source) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Kind != b.Kind || a.Name != b.Name || a.StepID != b.StepID {
		return false
	}
	if len(a.Path) != len(b.Path) {
		return false
	}
	for i := range a.Path {
		if a.Path[i] != b.Path[i] {
			return false
		}
	}
	return true
}

func dedupeRefs(refs []ProposalRef) []ProposalRef {
	seen := map[string]bool{}
	out := make([]ProposalRef, 0, len(refs))
	for _, ref := range refs {
		if ref.ProposalID == "" || seen[ref.ProposalID] {
			continue
		}
		seen[ref.ProposalID] = true
		out = append(out, ref)
	}
	return out
}

// sortRefs fixes the plan's order. Canonical order is producer path then
// proposal id, which is a function of the graph and the recorded handles and
// nothing else.
//
// smallest_diff_first is the one declared order that is not that order, and
// the scheduler holds no diff-size fact: the size lives in the proposal
// runtime's change metadata, which the settlement envelope reports only after
// the disposition. So the plan is taken in canonical order and the run says so
// once in Result.Warnings, rather than silently redefining the author's order
// or refusing a legal declaration. The size fact is fan-out and registry-read
// plumbing (yaah-0u0v.14).
func sortRefs(refs []ProposalRef, order ir.SettleOrder) {
	if order == ir.OrderSmallestDiffFirst {
		sort.SliceStable(refs, func(i, j int) bool {
			return refs[i].ProposalID < refs[j].ProposalID
		})
		return
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Producer != refs[j].Producer {
			return refs[i].Producer < refs[j].Producer
		}
		return refs[i].ProposalID < refs[j].ProposalID
	})
}

// flatten renders a multi-line fact as one log field. The event log is a
// byte-stable one-line-per-event artifact, so no settlement prose — an error
// body or a conflict note — may put a newline into it.
func flatten(s string) string {
	if s == "" {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}
