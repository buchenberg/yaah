package exec

import (
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/trace"
)

// trace.go is the run's trace projection (yaah-0u0v.17): the one seam that
// turns a reducer event into the typed records the trace surface reads.
//
// The projection is deliberately NOT a second bookkeeping path. It runs inside
// commit, immediately after the event's canonical log line is appended, and it
// reads only:
//
//   - the event itself (every fact the driver reported),
//   - the immutable graph node the event names (id, kind, declared lane),
//   - the container a fan-out instance was minted by (its scope id),
//   - the output bindings the producing work already recorded.
//
// It reads no lifecycle state that the fold would change, which is why it can
// run BEFORE the switch: a record is a statement about the event, not about the
// state the event is about to produce, and ordering the two that way keeps the
// record sequence identical to the event sequence.
//
// # Derivation table
//
//	record kind        reducer event(s)                  which fields
//	workflow.started   run_started, run_resumed          status (started|resumed)
//	node.started       node_ready                        node id/kind/scope
//	dispatch           dispatch_started                  attempt, dispatch id,
//	                                                     lane, status=started
//	dispatch           attempt_completed                 attempt, dispatch id,
//	                                                     lane, envelope status,
//	                                                     stop reason (reason),
//	                                                     result, output digest
//	proposal           attempt_completed w/ proposal id  proposal id, attempt
//	settle             settlement_asked                  proposal id, action,
//	                                                     status=asked
//	settle             proposal_settled                  proposal id, action,
//	                                                     door status, reason
//	node.finished      node_succeeded                    status, output digest
//	                   node_failed, node_skipped,        status, reason,
//	                   node_cancelled                    output digest
//	workflow.ended     run_finished                      run status, reason
//
// Events that project no record are the ones whose consequence is already
// recorded by the records above: settlement_planned, fanout_expanded,
// loop_iteration_started, loop_completed, branch_routed, and aggregate_inputs
// are inputs the reducer turns into instances (whose node.started /
// node.finished records state the outcome), retry_scheduled is stated by the
// next dispatch record's attempt number, and budget_exceeded reaches the trace
// as the workflow.ended record's reason.

// recordTrace projects one event and appends its records to the run's trace. It
// is called from commit, after the event's log line is written, so
// Record.EventSeq and the log's sequence number are the same number.
func (r *Run) recordTrace(ev Event) {
	for _, rec := range r.traceRecords(ev) {
		rec.Seq = len(r.records) + 1
		rec.EventSeq = r.seq
		if rec.RunID == "" {
			rec.RunID = r.runID
		}
		if rec.SpecHash == "" {
			rec.SpecHash = r.specHash
		}
		r.records = append(r.records, rec)
	}
}

// traceRecords is the projection proper: a pure function of the event and the
// graph facts the run already holds.
func (r *Run) traceRecords(ev Event) []trace.Record {
	switch e := ev.(type) {
	case RunStartedEvent:
		return []trace.Record{{
			Kind:   trace.KindRunStarted,
			RunID:  r.traceRunID(e.RunID),
			Status: trace.StatusStarted,
		}}
	case RunResumedEvent:
		// A resume is the run entering execution again. The vocabulary has one
		// word for "this run's execution started here", so it is recorded as
		// workflow.started with the resume status rather than as an eighth kind
		// nobody documented.
		return []trace.Record{{
			Kind:   trace.KindRunStarted,
			RunID:  r.traceRunID(e.RunID),
			Status: trace.StatusResumed,
		}}
	case NodeReadyEvent:
		return []trace.Record{r.nodeRecord(trace.KindNodeStarted, e.NodePath, trace.StatusStarted)}
	case DispatchStartedEvent:
		if e.Attempt == nil {
			return nil
		}
		rec := r.nodeRecord(trace.KindDispatch, e.Attempt.NodePath, trace.StatusStarted)
		rec.Attempt = e.Attempt.Number
		rec.DispatchID = e.Attempt.DispatchID
		rec.Lane = r.laneOf(e.Attempt)
		return []trace.Record{rec}
	case AttemptCompletedEvent:
		return r.attemptRecords(e)
	case NodeSucceededEvent:
		rec := r.nodeRecord(trace.KindNodeFinished, e.NodePath, trace.StatusSucceeded)
		rec.OutputDigest = r.outputDigest(e.NodePath)
		return []trace.Record{rec}
	case NodeFailedEvent:
		rec := r.nodeRecord(trace.KindNodeFinished, e.NodePath, trace.StatusFailed)
		rec.Reason = e.Reason
		rec.OutputDigest = r.outputDigest(e.NodePath)
		return []trace.Record{rec}
	case NodeSkippedEvent:
		rec := r.nodeRecord(trace.KindNodeFinished, e.NodePath, trace.StatusSkipped)
		rec.Reason = e.Reason
		return []trace.Record{rec}
	case NodeCancelledEvent:
		rec := r.nodeRecord(trace.KindNodeFinished, e.NodePath, trace.StatusCancelled)
		rec.Reason = e.Reason
		rec.DispatchID = e.DispatchID
		if e.CancelErr != "" {
			// A cancel that failed is a fact about the world, not a state
			// change, and the node's own reason already says why it ended. Both
			// go on the one line so the record is not a trimmed copy of the
			// node's state.
			rec.Reason = rec.Reason + " (cancel failed: " + e.CancelErr + ")"
		}
		rec.OutputDigest = r.outputDigest(e.NodePath)
		return []trace.Record{rec}
	case SettlementAskedEvent:
		rec := r.nodeRecord(trace.KindSettle, e.NodePath, trace.StatusAsked)
		rec.ProposalID = e.ProposalID
		rec.Action = e.Action
		return []trace.Record{rec}
	case ProposalSettledEvent:
		rec := r.nodeRecord(trace.KindSettle, e.NodePath, e.Settled.Status)
		rec.ProposalID = e.Settled.ProposalID
		rec.Action = e.Settled.Action
		rec.Reason = settledReason(e)
		rec.Result = e.Settled.Outcome
		if e.Settled.Outcome != "" {
			rec.OutputDigest = trace.Digest(e.Settled.Outcome)
		}
		return []trace.Record{rec}
	case RunFinishedEvent:
		return []trace.Record{{
			Kind:   trace.KindRunEnded,
			Status: string(e.Status),
			Reason: e.Reason,
		}}
	}
	return nil
}

// attemptRecords projects one completion: the dispatch's own record, plus the
// proposal record when the attempt sealed a handle.
func (r *Run) attemptRecords(e AttemptCompletedEvent) []trace.Record {
	c := e.Completion
	rec := r.nodeRecord(trace.KindDispatch, e.NodePath, c.Status)
	rec.Attempt = e.Attempt
	rec.DispatchID = e.DispatchID
	rec.Lane = r.laneOfCompletion(e.NodePath, c.Lane)
	rec.Reason = firstSet(e.RuntimeError, c.StopReason, c.Error)
	rec.Result = c.Result
	rec.OutputDigest = trace.Digest(c.Result)
	if rec.Status == "" {
		// A door malfunction carries no envelope, so there is no status to
		// quote. It is not a completion the run accepted, and the record says
		// so with the one word the vocabulary has for it.
		rec.Status = trace.StatusRuntimeError
	}
	out := []trace.Record{rec}
	if c.ProposalID != "" {
		p := r.nodeRecord(trace.KindProposal, e.NodePath, c.Status)
		p.Attempt = e.Attempt
		p.DispatchID = e.DispatchID
		p.Lane = rec.Lane
		p.ProposalID = c.ProposalID
		p.OutputDigest = rec.OutputDigest
		out = append(out, p)
	}
	return out
}

// nodeRecord builds a record about one node, filling the identity facts the
// graph and the instance table already hold.
func (r *Run) nodeRecord(kind trace.Kind, path, status string) trace.Record {
	rec := trace.Record{Kind: kind, NodePath: path, Status: status}
	if n := r.byPath[path]; n != nil {
		rec.NodeID = n.ID
		rec.NodeKind = string(n.Kind)
		rec.Lane = string(n.Lane)
	}
	if inst := r.instances[path]; inst != nil {
		rec.ScopeID = inst.owner
	}
	return rec
}

// traceRunID prefers the id the event names, because a run_started may carry the
// id the caller minted, and falls back to the run's own.
func (r *Run) traceRunID(fromEvent string) string {
	if fromEvent != "" {
		return fromEvent
	}
	return r.runID
}

// laneOf echoes the attempt's declared lane, falling back to the graph's.
func (r *Run) laneOf(a *Attempt) string {
	if a == nil {
		return ""
	}
	if a.Lane != "" {
		return a.Lane
	}
	if n := r.byPath[a.NodePath]; n != nil {
		return string(n.Lane)
	}
	return ""
}

// laneOfCompletion reads the lane from the completion, then the graph: a lane
// that reported its own lane is quoted, and one that stayed silent states what
// the document declared.
func (r *Run) laneOfCompletion(path, lane string) string {
	if lane != "" {
		return lane
	}
	if n := r.byPath[path]; n != nil {
		return string(n.Lane)
	}
	return ""
}

// outputDigest is the content address of what a node produced: its last
// attempt's recorded result when it has one, and otherwise the recorded output
// bindings joined in declaration order (an emit or a settle records a value
// without dispatching anything).
func (r *Run) outputDigest(path string) string {
	st := r.state[path]
	if st == nil {
		return ""
	}
	if a := st.LastAttempt(); a != nil && a.Result != "" {
		return trace.Digest(a.Result)
	}
	if len(st.Outputs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, o := range st.Outputs {
		b.WriteString(o.Name)
		b.WriteString("\x00")
		b.WriteString(o.Value)
		b.WriteString("\x00")
	}
	return trace.Digest(b.String())
}

// settledReason states why a settlement ended the way it did: a disposition the
// scheduler refused to ask for, a door that malfunctioned, or the error a
// completed disposition reported.
func settledReason(e ProposalSettledEvent) string {
	if e.Refused != "" {
		return e.Refused
	}
	if e.DoorError != "" {
		return e.DoorError
	}
	return e.Settled.Error
}

// firstSet returns the first non-empty value, so a reason quotes the most
// specific cause available without the caller writing a nested if.
func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
