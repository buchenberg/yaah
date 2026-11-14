package exec

import (
	"context"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Execute drives the run to a terminal state and returns its result. It is the
// ordinary entry point: New, Execute, read the Result. Each cycle is one
// Advance, so a caller that needs to interleave with the door — a test moving a
// virtual clock, a host observing progress between dispatches — can drive
// Advance itself and read State at any point.
func (r *Run) Execute(ctx context.Context) (Result, error) {
	for !r.finished {
		progressed, err := r.Advance(ctx)
		if err != nil {
			return Result{}, err
		}
		if !progressed {
			// Defensive: Advance closes the run whenever it has nothing left
			// to do, so reaching here means the reducer was handed a graph it
			// cannot finish. Refuse rather than spin or hang.
			return Result{}, fmt.Errorf("workflow/exec: run %s made no progress with %d node(s) unresolved",
				r.runID, r.unresolved())
		}
	}
	res := r.Result()
	if res.Status == RunRunning {
		return Result{}, fmt.Errorf("workflow/exec: run %s finished without a disposition", r.runID)
	}
	return res, nil
}

// Advance performs one scheduler cycle: sample the budgets, start what the
// ready queue and the concurrency cap allow, then commit the oldest
// outstanding attempt's completion. It reports whether the cycle did anything;
// a cycle that does nothing means the run is finished.
//
// The driver never mutates lifecycle state. It reads the reducer, calls the
// door, and submits the resulting event — which is why the same sequence comes
// out of a real adapter and a scripted fake.
func (r *Run) Advance(ctx context.Context) (bool, error) {
	if r.finished {
		return false, nil
	}
	if !r.started {
		return true, r.Apply(RunStartedEvent{RunID: r.runID})
	}
	if err := ctx.Err(); err != nil {
		return true, r.stop(RunCancelled, "context ended: "+err.Error())
	}
	if r.stopping {
		// An abort, a budget breach, or a cancel: the run stops scheduling,
		// and what is still in flight is cancelled through the door rather
		// than abandoned.
		return true, r.stop(r.status, r.reason)
	}
	if axis, limit, ok := r.breach(); ok {
		return true, r.Apply(BudgetExceededEvent{Axis: axis, Limit: limit})
	}
	started, err := r.startReady(ctx)
	if err != nil {
		return true, err
	}
	if started {
		return true, nil
	}
	// Then the settlement door: one plan report or one disposition per cycle,
	// so a settle node's work gets its own place in the log and its own
	// scheduler cycle. This runs before the "nothing left to do" test, because
	// a ready settle node is progress the run has not made yet — and a
	// settlement starts no attempt, so it appears nowhere in the dispatch
	// tally this cycle would otherwise consult.
	if settling, serr := r.settleStepOnce(ctx); settling {
		return true, serr
	}
	// Then the fan-out door: one expansion per cycle, after the settle cycle so
	// a ready settlement still gets its own place in the log. An expansion
	// dispatches nothing itself, so it appears nowhere in the dispatch tally
	// this cycle would otherwise consult.
	if expanding, eerr := r.fanoutStepOnce(); expanding {
		return true, eerr
	}
	if len(r.outstanding) == 0 {
		// Nothing running and nothing startable: the part of the graph this run
		// can reach has terminated, and anything still pending is stranded
		// behind a need that never terminates — WF004's finding, which the
		// scheduler finalizes as cancelled instead of waiting for.
		return true, r.stop(r.disposition(), r.dispositionReason())
	}
	return r.commitHead(ctx)
}

// Cancel stops the run from the outside: every running attempt is cancelled
// through the door, every node that never ran is cancelled by the close, and
// the run finishes with RunCancelled. Cancel takes no context — teardown is
// unconditional, for the reason stop documents. Call Cancel from the same
// goroutine that drives Advance; it is not a signal to a concurrent loop.
func (r *Run) Cancel(reason string) error {
	if r.finished {
		return nil
	}
	if reason == "" {
		reason = "cancelled"
	}
	return r.stop(RunCancelled, reason)
}

// startReady hands attempts to the door while the cap has room, taking ready
// nodes in the queue's canonical order. It starts at most one attempt per call
// so that a cap saturation, a budget breach, and a completion commit each get
// their own cycle and their own place in the log.
func (r *Run) startReady(ctx context.Context) (bool, error) {
	if !r.slotFree() {
		return false, nil
	}
	path, ok := r.nextStartable()
	if !ok {
		return false, nil
	}
	n := r.byPath[path]
	st := r.state[path]
	number := len(st.Attempts) + 1
	if l := r.cfg.Limits; l.MaxDispatches > 0 && r.dispatches+1 > l.MaxDispatches {
		return true, r.Apply(BudgetExceededEvent{
			Axis:  AxisMaxDispatches,
			Limit: fmt.Sprintf("%d", l.MaxDispatches),
		})
	}
	a := &Attempt{
		Number:     number,
		NodePath:   path,
		DispatchID: dispatchID(path, number),
		Role:       n.Role,
		Lane:       string(n.Lane),
		StartedAt:  r.rt.Now(),
	}
	if wait := r.backoff[path]; wait > 0 {
		// The delay is honored through the door, so a fake advances a virtual
		// clock and a test never sleeps. A backoff that fails is a door
		// malfunction on a node that has not started.
		if err := r.rt.Backoff(ctx, wait); err != nil {
			return true, r.Apply(AttemptCompletedEvent{
				NodePath:     path,
				DispatchID:   a.DispatchID,
				Attempt:      a.Number,
				RuntimeError: fmt.Sprintf("Runtime.Backoff: %v", err),
			})
		}
		a.Backoff = wait
	}
	h, err := r.rt.Start(ctx, Dispatch{
		Node:       n,
		Attempt:    a.Number,
		DispatchID: a.DispatchID,
		Scope:      r.snapshot(path, a.Number),
		Timeout:    nodeTimeout(n),
		Deadline:   r.deadline(),
	})
	if err != nil {
		return true, r.Apply(AttemptCompletedEvent{
			NodePath:     path,
			DispatchID:   a.DispatchID,
			Attempt:      a.Number,
			RuntimeError: fmt.Sprintf("Runtime.Start: %v", err),
		})
	}
	a.Handle = h
	if err := r.Apply(DispatchStartedEvent{Attempt: a}); err != nil {
		return true, err
	}
	r.outstanding = append(r.outstanding, a)
	return true, nil
}

// commitHead blocks until the oldest outstanding attempt can be committed, and
// commits nothing else first. Waiting is bounded by the door: a Block that
// returns without anything becoming completable is reported, not retried,
// because a scheduler that spins on a malfunctioning door turns a bug into an
// outage.
func (r *Run) commitHead(ctx context.Context) (bool, error) {
	for {
		if len(r.outstanding) == 0 {
			return false, nil
		}
		head := r.outstanding[0]
		if c, ok := r.rt.Completed(head.Handle); ok {
			r.popOutstanding()
			err := r.Apply(AttemptCompletedEvent{
				NodePath:   head.NodePath,
				DispatchID: head.DispatchID,
				Attempt:    head.Number,
				Completion: c,
			})
			return true, err
		}
		if err := ctx.Err(); err != nil {
			return true, r.stop(RunCancelled, "context ended: "+err.Error())
		}
		if err := r.rt.Block(ctx); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return true, r.stop(RunCancelled, "context ended: "+cerr.Error())
			}
			return true, r.abandonHead(head, fmt.Sprintf("Runtime.Block: %v", err))
		}
		if r.anyCompleted() {
			continue
		}
		return true, r.abandonHead(head, "Runtime.Block returned with no completable attempt")
	}
}

// abandonHead ends the attempt the run cannot get news about. The node's
// dispatch already counted against the budget, so the tally is not rolled back;
// what is reported is that the door stopped answering.
func (r *Run) abandonHead(a *Attempt, msg string) error {
	r.popOutstanding()
	return r.Apply(AttemptCompletedEvent{
		NodePath:     a.NodePath,
		DispatchID:   a.DispatchID,
		Attempt:      a.Number,
		RuntimeError: msg,
	})
}

// popOutstanding drops a committed attempt from the driver's queue. A dropped
// attempt is never polled again, so an envelope that arrives after the run
// already cancelled or terminated the node cannot reach Apply out of order:
// the reducer's state is authoritative and the log stays canonical.
func (r *Run) popOutstanding() {
	r.outstanding = r.outstanding[1:]
}

func (r *Run) anyCompleted() bool {
	for _, a := range r.outstanding {
		if _, ok := r.rt.Completed(a.Handle); ok {
			return true
		}
	}
	return false
}

// stop cancels what is in flight through the door and closes the run. Order
// matters and is fixed: dispatch order for the cancels, canonical path order
// for everything the close has to terminate, so one graph yields one sequence.
// It takes no context by design: teardown is unconditional, and a caller whose
// context already ended still owes every open attempt its Cancel call —
// abandoning teardown halfway would orphan real dispatches.
func (r *Run) stop(status RunStatus, reason string) error {
	// The run is on its way out from here on: cancelling an item is not an
	// outcome its fan-out may fold into a success.
	r.closing = true
	for _, a := range append([]*Attempt(nil), r.outstanding...) {
		var cancelErr string
		if err := r.rt.Cancel(a.Handle); err != nil {
			cancelErr = err.Error()
		}
		if err := r.Apply(NodeCancelledEvent{
			NodePath:   a.NodePath,
			DispatchID: a.DispatchID,
			Reason:     ReasonCancelledByRun + ": " + reason,
			CancelErr:  cancelErr,
		}); err != nil {
			return err
		}
	}
	r.outstanding = nil
	return r.Apply(RunFinishedEvent{Status: status, Reason: reason})
}

// slotFree reports whether the concurrency ceiling has room. An undeclared
// ceiling is unbounded, and an in-flight count held by the reducer is the only
// number consulted — the driver cannot start a second attempt than the cap
// allows, and the reducer refuses one if it ever tried.
func (r *Run) slotFree() bool {
	cap := r.cfg.Limits.MaxConcurrentDispatches
	if cap <= 0 {
		return true
	}
	return r.inFlight < cap
}

// nextStartable returns the first ready node the kind switch dispatches AND
// whose fan-out has room under its own max_parallel. The ready queue is kept
// sorted by node path, so this is canonical rather than insertion-ordered: two
// runs of the same graph with the needs declared in a different order still
// start the same node first, and an item throttled by its fan-out's nested cap
// is skipped for the next startable node rather than blocking the run.
func (r *Run) nextStartable() (string, bool) {
	for _, p := range r.ready {
		if !dispatched(r.byPath[p]) {
			continue
		}
		if !r.itemSlotFree(p) {
			continue
		}
		return p, true
	}
	return "", false
}

// breach reports the exhausted budget axis, if any, in a fixed order: the
// dispatch ceiling first, then the wall clock. A run that trips two axes in the
// same cycle therefore names the same one every time.
func (r *Run) breach() (string, string, bool) {
	l := r.cfg.Limits
	if l.MaxDispatches > 0 && r.dispatches >= l.MaxDispatches {
		return AxisMaxDispatches, fmt.Sprintf("%d", l.MaxDispatches), true
	}
	if l.MaxWallClock > 0 && !r.startedAt.IsZero() {
		if elapsed := r.rt.Now().Sub(r.startedAt); elapsed >= l.MaxWallClock {
			return AxisMaxWallClock, l.MaxWallClock.String(), true
		}
	}
	return "", "", false
}

// deadline converts the wall-clock ceiling into the instant the lane should
// stop at, for the adapter to enforce in flight. The scheduler has no timers,
// so it samples the clock at event boundaries (breach) and hands the deadline
// to the only component that can act on it mid-dispatch.
func (r *Run) deadline() time.Time {
	l := r.cfg.Limits
	if l.MaxWallClock <= 0 || r.startedAt.IsZero() {
		return time.Time{}
	}
	return r.startedAt.Add(l.MaxWallClock)
}

// disposition computes the run's status once nothing is left to schedule. The
// scan is over canonical node order so the answer, and the reason naming it,
// never depend on map order. Fan-out items are scanned too: an item that ended
// failed under on_item_failure: continue is the same fact as a node that ended
// failed under on_failure: continue — work was lost and the run drains with the
// loss stated rather than reporting success over it.
func (r *Run) disposition() RunStatus {
	if r.stopping {
		return r.status
	}
	for _, p := range r.allPaths() {
		if r.state[p].State == StateFailed {
			return RunFailed
		}
	}
	for _, p := range r.allPaths() {
		if r.state[p].State == StateCancelled {
			return RunCancelled
		}
	}
	return RunSucceeded
}

func (r *Run) dispositionReason() string {
	if r.stopping {
		return r.reason
	}
	for _, p := range r.allPaths() {
		if r.state[p].State == StateFailed {
			return r.exposedFailureReason(p)
		}
	}
	if stranded := r.unresolved(); stranded > 0 {
		return fmt.Sprintf("%d node(s) never became ready", stranded)
	}
	return "all nodes terminated"
}

// exposedFailureReason states why one failed node is a run-level failure rather
// than an abort. The two cases are different declarations, and the reason says
// which one was made.
func (r *Run) exposedFailureReason(path string) string {
	if inst := r.instances[path]; inst != nil {
		return path + ": fan-out item failed under on_item_failure: " + itemFailureString(r.byPath[inst.owner]) +
			" — " + r.state[path].Reason
	}
	return path + ": on_failure: continue exposed the failure — " + r.state[path].Reason
}

// unresolved counts nodes that never reached a terminal state.
func (r *Run) unresolved() int {
	var n int
	for _, p := range r.allPaths() {
		if !r.state[p].State.Terminal() {
			n++
		}
	}
	return n
}

// allPaths returns every node path this run holds in result order: a top-level
// node in canonical order, each followed by the instances its fan-out items
// created (and, recursively, theirs). It is the one iteration order every
// whole-run scan uses — the close, the disposition, the scope, and the result —
// so a graph yields one order everywhere and no scan can quietly miss an
// instance.
func (r *Run) allPaths() []string {
	out := make([]string, 0, len(r.paths)+len(r.instances))
	var emit func(path string)
	emit = func(path string) {
		out = append(out, path)
		for _, ip := range r.instanceOf[path] {
			emit(ip)
		}
	}
	for _, p := range r.paths {
		emit(p)
	}
	return out
}

// snapshot builds the immutable run-state view one attempt is dispatched
// against. It reads the reducer and mutates nothing.
func (r *Run) snapshot(path string, attempt int) ir.Scope {
	s := &ScopeSnapshot{
		runID:   r.runID,
		attempt: attempt,
		params:  r.params,
		outputs: map[string]any{},
		collect: map[string]any{},
		ids:     map[string]string{},
	}
	for _, p := range r.allPaths() {
		st := r.state[p]
		n := r.byPath[p]
		if n == nil || st.State != StateSucceeded {
			continue
		}
		// The recorded value is the node's own typed value, not only its text:
		// a proposal-lane delegate answers for `steps.<id>.output.proposal_id`,
		// a structured Report for `steps.<id>.output.<field>`, a settle for
		// `steps.<id>.output.settled`, and a fan-out for its collected items.
		// Run.stepValue is the one place those readings are decided, so a
		// template and a settlement handle can never disagree about what the
		// same step produced.
		s.outputs[p] = r.stepValue(n, st)
		// Only a top-level step id is reachable by `steps.<id>`: an instance's
		// id is body-local, so two items can share one and only the item's own
		// scope may resolve it (locals, below).
		if n.ID != "" && r.instances[p] == nil {
			s.ids[n.ID] = p
		}
		if n.Kind == ir.KindFanout && n.Collect != "" {
			s.collect[p] = st.Collect
		}
	}
	if inst := r.instances[path]; inst != nil {
		s.locals = inst.locals
		s.items = r.itemBindings(path)
	}
	return s
}

func dispatchID(path string, attempt int) string {
	return fmt.Sprintf("%s#%d", path, attempt)
}

// settleStepOnce drives one settlement cycle: the driver finds the first
// ready settle node, calls the door (or reports the plan it resolved from run
// state), and submits the resulting event. It mutates nothing itself, which is
// why a scripted fake and the real dispatch lane produce the same sequence.
//
// A cycle that reports no progress is a node still waiting on a producer that
// has not terminated: the run then falls through to whatever else it can do,
// and a node whose producer never terminates is terminated by the close as
// cancelled rather than settled against an input that does not exist.
func (r *Run) settleStepOnce(ctx context.Context) (bool, error) {
	path, ok := r.nextSettlePath()
	if !ok {
		return false, nil
	}
	return r.settleStep(ctx, path)
}
