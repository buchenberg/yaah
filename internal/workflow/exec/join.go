package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Join execution (yaah-0u0v.19).
//
// join: all is the fold a fan-out has always performed: every item must deliver
// before the container closes. join: any and join: quorum ask a different
// question — "has ENOUGH arrived" — and that difference is the whole of this
// file:
//
//   - the container closes as soon as the join is SATISFIED: the first item
//     success wins under any, the Nth under quorum. The winner set is fixed at
//     that instant, so the collected value is the winners' values and nothing
//     that arrives afterwards can widen it;
//   - whatever the close leaves behind is DRAINED or CANCELLED through the door
//     (clearOrphanItems), never abandoned silently. An attempt the door has
//     already finished is a RESULT and is committed — its envelope may name a
//     sealed proposal, and a dropped handle is a worktree nobody disposes of —
//     while an attempt still running is stopped with Runtime.Cancel and its late
//     envelope dropped, which is exactly the contract Runtime.Cancel documents;
//   - a proposal a NON-WINNING item sealed is discarded through the door's one
//     settlement path (discardStepOnce): the same Runtime.Settle verb a settle
//     node asks for. Nothing here settles, discards, or merges anything itself.
//
// Cancellation is cooperative and stays inside the single-threaded reducer. The
// reducer decides inside Apply that a join is satisfied; the DRIVER performs the
// door calls in the cycle after. No goroutine is started, no channel is opened,
// and no work is orphaned: an abandoned item is a node in a terminal state with
// a stated reason, and an abandoned proposal is a settlement the door reported.
//
// # Why a join: all container keeps its old behaviour
//
// Nothing in this file fires for join: all. Its container closes when its LAST
// item terminates, so it never has residue to drain, never cancels an item, and
// never discards a proposal — a proposal any item of it sealed is the graph's
// own business, settled (or refused) by a settle node and the run-level guard the
// way yaah-0u0v.13 built it.

// Reasons the join family states.
const (
	// ReasonFanoutJoinUnmet prefixes a container whose join can no longer be
	// satisfied: enough items have terminated without succeeding that the
	// remaining ones cannot make up the count. Waiting for them would be waiting
	// for an answer the run can already prove it will never get.
	ReasonFanoutJoinUnmet = "fanout_join_unmet"
	// ReasonFanoutQuorumInvalid names a join: quorum declaration that is not a
	// usable count — no quorum at all, one below 1, or one above the number of
	// items the `over` source resolved to. It is a declaration refusal, so it is
	// stated before a single item dispatches.
	ReasonFanoutQuorumInvalid = "fanout_quorum_invalid"
	// ReasonFanoutJoinAbandoned is the reason an item the join did not wait for
	// carries: the container is already terminal, so this item's work is recorded
	// and never folded into the container's value.
	ReasonFanoutJoinAbandoned = "fanout_join_abandoned_item"
)

// joinKind reads a fan-out's join with the folded default: an author who declared
// none declared "all of them must deliver".
func joinKind(n *ir.Node) ir.Join {
	if n == nil || n.Join == "" {
		return ir.JoinAll
	}
	return n.Join
}

// cancelJoin reports whether a join closes EARLY, leaving work behind. any and
// quorum both stop as soon as enough items delivered; all closes on the last
// item and therefore abandons nothing.
func cancelJoin(n *ir.Node) bool {
	switch joinKind(n) {
	case ir.JoinAny, ir.JoinQuorum:
		return true
	}
	return false
}

// joinThreshold is how many item successes satisfy the join. Zero means the join
// is not a count: join: all closes on its last item and no tally satisfies it.
func joinThreshold(n *ir.Node) int {
	switch joinKind(n) {
	case ir.JoinAny:
		return 1
	case ir.JoinQuorum:
		if n != nil && n.Quorum != nil && *n.Quorum >= 1 {
			return *n.Quorum
		}
	}
	return 0
}

// quorumRefusal states a quorum the resolved item list cannot satisfy. The upper
// bound is a RUN fact (how many items `over` produced) while the lower bound is a
// DECLARATION fact classifyKind already refused, so the check lives here, where
// both are known, and refuses before the first item dispatches rather than
// letting the container fail halfway as a runtime surprise.
func quorumRefusal(n *ir.Node, items int) string {
	if joinKind(n) != ir.JoinQuorum {
		return ""
	}
	need := joinThreshold(n)
	if need <= 0 {
		return "" // classifyKind already refused a missing or non-positive count
	}
	if need > items {
		return ReasonFanoutQuorumInvalid + ": " + n.Path + " declares quorum: " + strconv.Itoa(need) +
			" but over resolved to " + strconv.Itoa(items) +
			" item(s); a quorum is at least 1 and at most the number of items"
	}
	return ""
}

// joinLabel renders a join for a reason line, naming the count a quorum needs so
// a reader does not have to look it up.
func joinLabel(n *ir.Node) string {
	j := string(joinKind(n))
	if joinKind(n) == ir.JoinQuorum {
		if need := joinThreshold(n); need > 0 {
			j += " " + strconv.Itoa(need)
		}
	}
	return j
}

// joinCensus folds every item that has reached an outcome and reports the item
// successes so far, in item order, plus how many items could still become one.
//
// Only a TERMINAL item is folded: foldScope discounts nodes that have not
// terminated, so folding an item mid-flight would call a half-run body succeeded
// and a join could be "satisfied" by work that never happened. The order is the
// authored item order, which is what makes the winner set a function of which
// items succeeded and not of the order this scan happened to run in.
func (r *Run) joinCensus(st *NodeState) (winners []int, remaining int) {
	for i := range st.Items {
		it := &st.Items[i]
		if it.State == StatePending && r.scopeTerminal(it.Paths) {
			r.itemOutcome(it)
		}
		switch it.State {
		case StateSucceeded:
			winners = append(winners, i)
		case StatePending:
			remaining++
		}
	}
	return winners, remaining
}

// noteJoinItem is the join:any|quorum half of the container's reaction to one of
// its instances terminating. It folds the item, asks the census where the join
// stands, and answers one of three things: the join is satisfied (close on the
// winners), the join can no longer be satisfied (fail, naming the count), or the
// container is still waiting.
//
// An ITEM FAILURE does not fail the container here, and that is deliberate: any
// and quorum are declarations that N-of-M delivery is enough, so a failed item is
// one of the M that did not deliver rather than a stop condition. The author's
// on_item_failure disposition still governs join: all, where "some item failed" is
// not something the join absorbs.
func (r *Run) noteJoinItem(f *ir.Node, st *NodeState) error {
	need := joinThreshold(f)
	if need <= 0 {
		need = 1
	}
	winners, remaining := r.joinCensus(st)
	if len(winners) >= need {
		return r.closeFanoutJoin(f, st, winners[:need])
	}
	if len(winners)+remaining < need {
		return r.failTerminal(f.Path, ReasonFanoutJoinUnmet+": "+fmt.Sprintf(
			"%s: join %s cannot be satisfied — %d of %d item(s) terminated and %d can still succeed, which is fewer than the %d the join requires",
			f.Path, joinLabel(f), len(st.Items)-remaining, len(st.Items), len(winners)+remaining, need))
	}
	return nil
}

// closeFanoutJoin closes a fan-out whose join any|quorum is satisfied.
//
// The winner set is FIXED here: collect is the winners' values in item order, so
// it never depends on which item the door happened to finish first, and a result
// that arrives later is recorded on its own item without widening it. The
// remaining items are not cancelled here — the reducer states the outcome, and
// the driver performs the door calls in the next cycle (clearOrphanItems).
func (r *Run) closeFanoutJoin(n *ir.Node, st *NodeState, winners []int) error {
	st.Winners = append([]int(nil), winners...)
	collect := make([]any, 0, len(winners))
	for _, j := range winners {
		collect = append(collect, st.Items[j].Value)
	}
	st.Collect = collect
	encoded, err := json.Marshal(collect)
	if err != nil {
		return r.failTerminal(n.Path, ReasonFanoutCollectFailed+": encoding the collected value: "+err.Error())
	}
	st.Result = string(encoded)
	r.recordOutput(st, NodeOutput{
		NodePath: n.Path,
		Kind:     n.Kind,
		Name:     fanoutBindingName(n),
		Value:    st.Result,
	})
	r.warnOnce("fanout-join:"+n.Path, fmt.Sprintf(
		"%s: join %s satisfied by item(s) %s of %d; the remaining item(s) are drained or cancelled and never change the collected value",
		n.Path, joinLabel(n), joinIndices(winners), len(st.Items)))
	return r.derive(NodeSucceededEvent{NodePath: n.Path, Outputs: outputNames(st)})
}

// joinIndices renders a winner set for a reason line.
func joinIndices(idx []int) string {
	parts := make([]string, 0, len(idx))
	for _, i := range idx {
		parts = append(parts, strconv.Itoa(i))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

// containsInt reports whether idx holds i, for the winner-set membership tests.
func containsInt(idx []int, i int) bool {
	for _, v := range idx {
		if v == i {
			return true
		}
	}
	return false
}

// enclosingCancelJoin returns the nearest enclosing fan-out with join any|quorum
// that has already TERMINATED, and the item index path belongs to at that level.
// Everything under such a container is residue: the join is decided, so an item
// that has not terminated is work nobody will collect, and the container's own
// outcome is the run-level fact rather than the item's.
func (r *Run) enclosingCancelJoin(path string) (container string, item int, ok bool) {
	for {
		in := r.instances[path]
		if in == nil {
			return "", 0, false
		}
		n := r.byPath[in.owner]
		st := r.state[in.owner]
		if n != nil && n.Kind == ir.KindFanout && cancelJoin(n) && st != nil && st.State.Terminal() {
			return in.owner, in.item, true
		}
		path = in.owner
	}
}

// inJoinResidue reports whether a path is an item of a decided join container.
// Such an item's outcome is not a run-level fact — the container's is — which is
// what keeps a failed item under join: any from reporting the whole run failed,
// and a cancelled loser from reporting the whole run cancelled.
func (r *Run) inJoinResidue(path string) bool {
	_, _, ok := r.enclosingCancelJoin(path)
	return ok
}

// clearOrphanItems drains or cancels every item a decided join left behind. It
// runs BEFORE the ready queue is consulted, so a body node released by a
// cancelled sibling is never dispatched: cancelling work and then starting more
// of it in the same cycle is the leak this step exists to prevent.
func (r *Run) clearOrphanItems() (bool, error) {
	cleared := false
	for _, p := range r.allPaths() {
		st := r.state[p]
		if st == nil || st.State.Terminal() || r.instances[p] == nil {
			continue
		}
		if _, _, ok := r.enclosingCancelJoin(p); !ok {
			continue
		}
		if err := r.clearOrphanItem(p); err != nil {
			return true, err
		}
		cleared = true
	}
	return cleared, nil
}

// clearOrphanItem ends one item of a decided join. Which of the two ways depends
// on the door, not on a preference:
//
//   - an attempt whose completion the door already holds is COMMITTED. The work
//     is done, so cancelling it would not stop anything; it would only drop an
//     envelope that may carry a proposal id, and a handle nobody read is a
//     worktree nobody can dispose of. Its outcome is recorded on the item and
//     never folded into the container.
//   - an attempt still in flight is CANCELLED through Runtime.Cancel, and its
//     envelope — if the world ever produces one — is dropped, which is what the
//     door contract says a cancel means and why nothing waits for it.
//   - a node that never started is cancelled outright: no door call, because
//     nothing outside this run is running it.
func (r *Run) clearOrphanItem(path string) error {
	reason := ReasonFanoutJoinAbandoned + ": " + path +
		" is not part of its container's decided join; its work is not collected"
	if a := r.current[path]; a != nil {
		if c, done := r.rt.Completed(a.Handle); done {
			r.dropOutstanding(path)
			return r.Apply(AttemptCompletedEvent{
				NodePath:   path,
				DispatchID: a.DispatchID,
				Attempt:    a.Number,
				Completion: c,
			})
		}
		var cancelErr string
		if err := r.rt.Cancel(a.Handle); err != nil {
			cancelErr = err.Error()
		}
		r.dropOutstanding(path)
		return r.Apply(NodeCancelledEvent{
			NodePath:   path,
			DispatchID: a.DispatchID,
			Reason:     reason,
			CancelErr:  cancelErr,
		})
	}
	return r.Apply(NodeCancelledEvent{NodePath: path, Reason: reason})
}

// pendingDiscard returns the next proposal the run owes a disposition to because
// a NON-WINNING item sealed it, along with the container the ask is attributed
// to. It is derived from the ledger rather than from a plan of its own: the run's
// open set already names every proposal it knows about and its owner, so a plan
// would be a second answer to the same question.
//
// A WINNER's proposal is deliberately not here. Under any/quorum the winner is
// the item the run is collecting, so what happens to its proposal is the graph's
// declaration — a settle node's apply, or the run-level refusal that says the
// author never declared one. Discarding it automatically would throw away exactly
// the work the join selected.
func (r *Run) pendingDiscard() (nodePath string, ref ProposalRef, ok bool) {
	for _, id := range r.openProposals() {
		owner := r.open[id]
		if r.instances[owner] == nil {
			continue
		}
		container, item, under := r.enclosingCancelJoin(owner)
		if !under {
			continue
		}
		if _, claimed := r.claims[id]; claimed {
			continue
		}
		st := r.state[container]
		if st == nil || containsInt(st.Winners, item) || alreadyDiscarded(st, id) {
			continue
		}
		return container, ProposalRef{ProposalID: id, Producer: owner}, true
	}
	return "", ProposalRef{}, false
}

// alreadyDiscarded reports whether a container already holds a disposition record
// for one proposal. A record whose settlement did not land keeps the id OUT of the
// retry set: asking twice for one disposition is the thing WF021 forbids, and a
// proposal left open is a fact the run's close states rather than papers over.
func alreadyDiscarded(st *NodeState, id string) bool {
	for _, s := range st.Discards {
		if s.ProposalID == id {
			return true
		}
	}
	return false
}

// discardStepOnce disposes of one proposal a non-winning item sealed, through the
// door's existing settlement path: the write-ahead ask is committed first, then
// Runtime.Settle is asked for the discard verb, then the disposition is folded.
// It is settleStep's shape for the case the graph declared no settle node for.
func (r *Run) discardStepOnce(ctx context.Context) (bool, error) {
	path, ref, ok := r.pendingDiscard()
	if !ok {
		return false, nil
	}
	verb, ok := settlementVerb(ir.SettleDiscard)
	if !ok {
		return false, nil // the four-verb vocabulary is closed; discard is in it
	}
	if err := r.Apply(SettlementAskedEvent{NodePath: path, ProposalID: ref.ProposalID, Action: verb}); err != nil {
		return true, err
	}
	s, err := r.rt.Settle(ctx, SettleRequest{
		NodePath:   path,
		ProposalID: ref.ProposalID,
		Producer:   ref.Producer,
		Verb:       verb,
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
		// A door malfunction has no envelope behind it, so the scheduler asserts
		// nothing about what the world did; the record says so and the proposal
		// stays open.
		ev.DoorError = err.Error()
		if s.Status == "" {
			s.Status = SettlementStatusError
		}
		ev.Settled = s
	}
	return true, r.Apply(ev)
}

// onJoinDiscardAsked records the discard a join container is about to ask the
// door for. It is the settlement write-ahead record for this path: with a Store
// configured the ask is durable BEFORE the world effect can happen, so a resumed
// run can tell "asked, outcome unknown" from "never asked" and refuses to ask a
// second time (see Resume).
func (r *Run) onJoinDiscardAsked(path string, st *NodeState, e SettlementAskedEvent) error {
	if st.Asked != "" {
		return fmt.Errorf("workflow/exec: settlement_asked for %s while proposal %s is still unanswered",
			path, st.Asked)
	}
	if e.ProposalID == "" {
		return fmt.Errorf("workflow/exec: settlement_asked for %s with no proposal id", path)
	}
	st.Asked = e.ProposalID
	return nil
}

// onJoinDiscardSettled folds one disposal of a non-winning item's proposal. It
// records the disposition on the container, discharges the write-ahead ask, and
// keeps the run's ledger honest: a settled discard takes the proposal out of the
// open set, and anything else leaves it there so the close refuses to call the run
// a success over work that is still standing.
func (r *Run) onJoinDiscardSettled(path string, st *NodeState, e ProposalSettledEvent) error {
	s := e.Settled
	if s.ProposalID == "" {
		s.ProposalID = st.Asked
	}
	st.Asked = ""
	if s.ProposalID != "" {
		st.Discards = append(st.Discards, s)
	}
	failure := e.DoorError
	if failure == "" {
		failure = e.Refused
	}
	if failure != "" {
		r.warnOnce("join-discard-door:"+path+":"+s.ProposalID, fmt.Sprintf(
			"%s: discarding proposal %s sealed by a non-winning item failed: %s; it stays open and the run will not report success over it",
			path, s.ProposalID, flatten(failure)))
		return nil
	}
	if s.Status == SettlementStatusSettled && !s.LeftOpen {
		r.settled[s.ProposalID] = path
		delete(r.open, s.ProposalID)
		return nil
	}
	r.warnOnce("join-discard-open:"+path+":"+s.ProposalID, fmt.Sprintf(
		"%s: discarding proposal %s sealed by a non-winning item reported status=%s%s; it stays open and the run will not report success over it",
		path, s.ProposalID, orDash(s.Status), conflictSuffix(s)))
	return nil
}

// conflictSuffix renders the overlap paths a refused discard named, for the
// warning above.
func conflictSuffix(s Settled) string {
	if len(s.Conflicts) == 0 {
		return ""
	}
	return " paths=" + strings.Join(s.Conflicts, ",")
}
