package exec

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// abort.go is the abort-time disposition pass (yaah-0u0v.24). A run that ends
// without succeeding may still hold sealed proposals with no disposition: the
// work landed, the settle that would have disposed of it never ran. The
// workflow's on_abort declaration says what becomes of them, and this pass is
// the whole implementation — an ADAPTER over the same settlement door the
// settle nodes use, not a second settlement engine:
//
//	halt          the default, declared or not: nothing is asked, and the
//	               close's reason names every proposal left open
//	release_all   ask the door to release each open proposal
//	discard_all   ask the door to discard each open proposal
//
// The pass runs from stop, after the in-flight attempts are cancelled and
// before the run is closed, and it applies to every non-succeeded close: an
// abort, a failure, a cancel, a budget breach — and the close the resume
// performs. A run that SUCCEEDED is not this pass's subject: its open
// proposals are the run-level linearity guard's (ReasonOpenProposalsAtExit),
// which is a success refusing to be reported, not a cleanup.
//
// Resume keeps the settlement discipline (yaah-0u0v.16): the ask is committed
// before the call, so a crash between the two leaves "asked, outcome unknown"
// on the record — and the ask record is never cleared, so a resumed pass asks
// the proposals that were never asked and never asks one twice. The door's
// one-disposition invariant owns the rest: whatever the first ask did, a run
// cannot dispose of one proposal twice.

// onAbortPolicy is the effective abort-time policy: what the document
// declared, or halt when it declared nothing. The default lives here and not
// in resolve, because the graph carries what the document said and never a
// rewrite of it.
func (r *Run) onAbortPolicy() ir.OnAbort {
	if p := r.graph.OnAbort; p != "" {
		return p
	}
	return ir.AbortHalt
}

// abortVerb maps the effective policy onto the door's verb. Halt asks for
// nothing.
func abortVerb(p ir.OnAbort) (string, bool) {
	switch p {
	case ir.AbortReleaseAll:
		return "release", true
	case ir.AbortDiscardAll:
		return "discard", true
	}
	return "", false
}

// disposeOnAbort applies the on_abort policy to every proposal the run still
// holds open. It is called from stop with the run's close already decided: a
// settled disposition leaves the open set, anything else leaves the proposal
// open for the close to name. stop's caller owns the loop that makes each
// abort disposition reach the door exactly once — this function asks in
// canonical order, so one run and one resume of it produce one sequence.
func (r *Run) disposeOnAbort(status RunStatus) error {
	if status == RunSucceeded {
		return nil
	}
	verb, ok := abortVerb(r.onAbortPolicy())
	if !ok {
		return nil // halt: the close's reason reports what it left open
	}
	// The door is asked under the context the run was last driven with, the
	// same one the settle steps settle under. A context that ended is not a
	// reason to skip a declared disposition — the door says what happened,
	// and an error disposition keeps the proposal open rather than
	// pretending the world agrees with the policy.
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for _, id := range r.openProposals() {
		if _, asked := r.abortAsked[id]; asked {
			continue // asked once in this run or a previous life of it
		}
		producer := r.open[id]
		if err := r.Apply(AbortAskedEvent{ProposalID: id, Action: verb}); err != nil {
			return err
		}
		s, err := r.rt.Settle(ctx, SettleRequest{
			ProposalID: id,
			Producer:   producer,
			Verb:       verb,
		})
		if s.ProposalID == "" {
			s.ProposalID = id
		}
		if s.Producer == "" {
			s.Producer = producer
		}
		s.Action = verb
		ev := AbortSettledEvent{Settled: s}
		if err != nil {
			// A door malfunction is an outcome reported as an error
			// disposition, exactly as a settle step records one: there is
			// no envelope behind it, so the proposal keeps whatever
			// state it had and the close names what is left open.
			ev.DoorError = err.Error()
			if s.Status == "" {
				s.Status = SettlementStatusError
			}
			s.LeftOpen = true
			ev.Settled = s
		}
		if err := r.Apply(ev); err != nil {
			return err
		}
	}
	return nil
}

// onAbortAsked records the abort-time disposition the run is about to ask the
// door for: the write-ahead half of the settlement discipline, with the ask
// kept permanently. A proposal leaves open only through an answer the door
// reported, so the record is never cleared — a resumed pass reads it to
// refuse a second ask for the same proposal from whichever side of a crash
// it finds itself.
func (r *Run) onAbortAsked(e AbortAskedEvent) error {
	if e.ProposalID == "" {
		return fmt.Errorf("workflow/exec: abort_asked with no proposal id")
	}
	if _, open := r.open[e.ProposalID]; !open {
		return fmt.Errorf("workflow/exec: abort_asked for proposal %s the run does not hold open", e.ProposalID)
	}
	if _, asked := r.abortAsked[e.ProposalID]; asked {
		return fmt.Errorf("workflow/exec: abort_asked for proposal %s twice; a proposal has exactly one disposition", e.ProposalID)
	}
	r.abortAsked[e.ProposalID] = e.Action
	return nil
}

// onAbortSettled folds one abort-time disposition into the ledger. It is the
// ledger half of onProposalSettled — there is no node to close and no plan
// to advance — so the bookkeeping is the whole fold: a settled disposition
// takes its permanent place and leaves the open set, anything else stays
// open for the close to report. The ask record is left standing: what it
// states ("this run asked once") survives the answer.
func (r *Run) onAbortSettled(e AbortSettledEvent) error {
	s := e.Settled
	if s.ProposalID == "" {
		return fmt.Errorf("workflow/exec: abort_settled with no proposal id")
	}
	if _, asked := r.abortAsked[s.ProposalID]; !asked {
		return fmt.Errorf("workflow/exec: abort_settled for proposal %s that was never asked for", s.ProposalID)
	}
	delete(r.claims, s.ProposalID)
	switch {
	case s.Status == SettlementStatusSettled && !s.LeftOpen:
		r.settled[s.ProposalID] = "on_abort"
		delete(r.open, s.ProposalID)
	default:
		if _, seen := r.open[s.ProposalID]; !seen {
			r.open[s.ProposalID] = s.Producer
		}
	}
	return nil
}
