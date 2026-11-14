package exec

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Run is one workflow run: the graph, the ceilings, the door, and all of the
// lifecycle state. A Run is driven by exactly one goroutine; the reducer needs
// no lock because there is nothing to lock against, and that is also what
// makes the event sequence reproducible.
type Run struct {
	runID string
	cfg   Config
	rt    Runtime
	graph *ir.Graph
	// specHash and specPath are the document identity this run was started
	// from, carried so every checkpoint states which spec the state belongs to
	// (yaah-0u0v.16). Both are informational to the scheduler: the hash is
	// compared at Resume, by the caller that already resolved a graph.
	specHash string
	specPath string
	// persisted counts the event log lines the Store has already recorded. It
	// is the high-water mark that keeps a checkpoint from re-writing the whole
	// log on every event.
	persisted int
	// ctx is the context the driver was entered with, used only for store
	// writes made from inside Apply. A store write races nothing and outlives no
	// run, but it does need a context, and Apply — the reducer's one mutator —
	// has none of its own by design.
	ctx context.Context

	nodes      []*ir.Node
	paths      []string // every top-level node path, sorted: the canonical order
	byPath     map[string]*ir.Node
	byID       map[string]*ir.Node
	state      map[string]*NodeState
	dependents map[string][]string // node path -> paths of the nodes that need it
	params     map[string]any

	// Fan-out instances (yaah-0u0v.14). instances holds every body node one
	// item minted, keyed by its instance path; instanceOf lists a container's
	// instance paths in creation order (item order, then body order), which is
	// the order the result reports; itemInFlight counts in-flight dispatches per
	// fan-out node path — the nested max_parallel tally, which the global cap
	// does not replace.
	instances    map[string]*instance
	instanceOf   map[string][]string
	itemInFlight map[string]int
	// arms is the ROUTING relation a branch owns (yaah-0u0v.15): an arm path
	// maps to the branches that may decide about it. It is kept beside the
	// dependency graph rather than inside it, because an arm may declare its
	// branch as a need and then it is both, while a branch that names an arm is
	// not a dependency of it.
	arms map[string]map[string]bool

	ready   []string // node paths in canonical order; the launch order
	current map[string]*Attempt
	backoff map[string]time.Duration

	// The settlement ledger. open holds the proposals this run sealed and has
	// not disposed of, claims the proposals a settle node is working through
	// right now, and settled the ones that reached a disposition — each keyed
	// proposal id -> the node path that owns or owned it.
	//
	// They live in the reducer because the linearity guards they serve must
	// hold under any event ordering: a double settle is refused from the
	// ledger, not from a scan of the graph, and a run that would end with a
	// proposal still in open is refused by the close itself.
	open    map[string]string
	claims  map[string]string
	settled map[string]string

	// warned keys the run warnings that must be stated once per graph.
	warned map[string]bool

	// outstanding is the driver's queue of started, uncommitted attempts in
	// dispatch order. It is door bookkeeping, not lifecycle state: no
	// scheduler decision reads it except "is there anything to wait for".
	outstanding []*Attempt

	events   []string
	warnings []string
	seq      int
	started  bool
	finished bool
	// resumed reports that the run was rebuilt from a checkpoint, which may
	// happen once per Run (yaah-0u0v.16).
	resumed bool

	dispatches   int
	inFlight     int
	peakInFlight int
	startedAt    time.Time
	finishedAt   time.Time
	status       RunStatus
	reason       string
	stopping     bool
	// closing reports that the run is on its way out. An item that terminates
	// while the run closes must not close its fan-out: the close is the run's
	// own answer, and a container declared succeeded mid-teardown would state a
	// disposition the close never made.
	closing bool
}

// New prepares a run over a resolved graph. Only the graph's top-level steps are
// scheduled here: a body node is entered through its container, so it becomes
// lifecycle state only when a fan-out expands into items (yaah-0u0v.14), and
// until then it is not silently skipped either — the container that owns it
// answers for it.
func New(g *ir.Graph, rt Runtime, cfg Config) (*Run, error) {
	if g == nil {
		return nil, errors.New("workflow/exec: nil graph")
	}
	if rt == nil {
		return nil, errors.New("workflow/exec: nil Runtime")
	}
	params, err := bindParams(g.Params, cfg.Params)
	if err != nil {
		return nil, err
	}
	r := &Run{
		cfg:          cfg,
		rt:           rt,
		graph:        g,
		params:       params,
		byPath:       map[string]*ir.Node{},
		byID:         map[string]*ir.Node{},
		state:        map[string]*NodeState{},
		dependents:   map[string][]string{},
		instances:    map[string]*instance{},
		instanceOf:   map[string][]string{},
		itemInFlight: map[string]int{},
		arms:         map[string]map[string]bool{},
		current:      map[string]*Attempt{},
		backoff:      map[string]time.Duration{},
		open:         map[string]string{},
		claims:       map[string]string{},
		settled:      map[string]string{},
		warned:       map[string]bool{},
		status:       RunRunning,
	}
	for i, n := range g.Steps {
		if n == nil {
			continue
		}
		if n.Path == "" {
			return nil, fmt.Errorf("workflow/exec: step %d has no node path", i)
		}
		if _, dup := r.byPath[n.Path]; dup {
			return nil, fmt.Errorf("workflow/exec: duplicate node path %q", n.Path)
		}
		r.nodes = append(r.nodes, n)
		r.byPath[n.Path] = n
		r.paths = append(r.paths, n.Path)
		if n.ID != "" {
			if _, dup := r.byID[n.ID]; !dup {
				r.byID[n.ID] = n
			}
		}
		st := &NodeState{
			Path:        n.Path,
			ID:          n.ID,
			Kind:        n.Kind,
			State:       StatePending,
			Needs:       append([]string(nil), n.Needs...),
			Policy:      effectivePolicy(n.OnFailure),
			MaxAttempts: attemptCeiling(n.Retry),
		}
		r.state[n.Path] = st
	}
	// The dependency graph is keyed by PATH, and it is built after every id is
	// registered: an id names a node in the scope it was compiled in, and the
	// one identity every node and every fan-out instance has is its path.
	for _, n := range r.nodes {
		needs, ok := r.needPaths(n)
		if !ok {
			continue // a need naming no step strands the node; reach.go reports it as WF004
		}
		for _, dep := range needs {
			r.dependents[dep] = append(r.dependents[dep], n.Path)
		}
	}
	// The routing relation is registered after every id for the same reason the
	// dependency graph is: an arm is named by step id, and a path is the one
	// identity every node and every instance has.
	for _, n := range r.nodes {
		r.registerArms(n)
	}
	sort.Strings(r.paths)
	for id := range r.dependents {
		sort.Strings(r.dependents[id])
	}
	if r.runID == "" {
		r.runID = cfg.RunID
	}
	if r.runID == "" {
		r.runID = "workflow:" + g.Name
	}
	r.specHash = cfg.SpecHash
	r.specPath = cfg.SpecPath
	// A guard the run cannot ask about is a fact the caller should not have to
	// infer from a node that ran anyway.
	for _, p := range r.paths {
		if n := r.byPath[p]; n.When != nil && cfg.Guard == nil {
			r.warnings = append(r.warnings, fmt.Sprintf(
				"%s declares a when guard but the run has no Guard: scheduled as if it passed", p))
		}
	}
	return r, nil
}

// effectivePolicy folds the one default the scheduler owns: an author who did
// not write on_failure did not authorize the run to proceed past a failure, so
// the disposition is abort. A silent "continue" would let a run keep working on
// top of a step that produced nothing, and WF021's proof assumes dispositions
// happen on every reachable path.
func effectivePolicy(p ir.FailurePolicy) ir.FailurePolicy {
	if p == "" {
		return ir.OnFailureAbort
	}
	return p
}

// warnOnce appends a run warning the first time a fact is observed and never
// again. Warnings are not part of the event log, so this cannot move a byte of
// the deterministic sequence; it exists so a declaration the run honored only
// partly is stated once instead of on every cycle that consults it.
func (r *Run) warnOnce(key, msg string) {
	if r.warned == nil {
		r.warned = map[string]bool{}
	}
	if r.warned[key] {
		return
	}
	r.warned[key] = true
	r.warnings = append(r.warnings, msg)
}

// attemptCeiling reads a node's retry block. An absent block, an absent
// max_attempts, or a non-positive one all mean one attempt: the ceiling is the
// author's, and the scheduler does not add retries nobody asked for.
func attemptCeiling(r *ir.Retry) int {
	if r == nil || r.MaxAttempts == nil || *r.MaxAttempts < 1 {
		return 1
	}
	return *r.MaxAttempts
}

func backoffFor(n *ir.Node) time.Duration {
	if n.Retry == nil || n.Retry.BackoffSeconds == nil || *n.Retry.BackoffSeconds <= 0 {
		return 0
	}
	return time.Duration(*n.Retry.BackoffSeconds * float64(time.Second))
}

func nodeTimeout(n *ir.Node) time.Duration {
	if n.TimeoutSeconds == nil || *n.TimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(*n.TimeoutSeconds) * time.Second
}

// Apply folds one event into the run's state. It is the only exported
// mutator: lifecycle transitions, the ready queue, the dispatch tally, and the
// event log change nowhere else, so the reducer is the single answer to "what
// state is this run in". Events that are consequences rather than reports —
// node_ready, node_succeeded, node_failed, node_skipped, retry_scheduled —
// are rejected here, because submitting a conclusion is how a parallel state
// view gets built.
//
// When a Store is configured, the checkpoint is written HERE, after the event is
// folded and before Apply returns (yaah-0u0v.16). That ordering is the whole
// idempotency argument: no attempt, proposal handle, or disposition can become
// visible to a resume without being durable first, so a run that stops at any
// boundary resumes from exactly the state it had committed. A store write fails
// the event: a run whose state cannot be recorded must not advance, because the
// next step would then re-do work the record does not know about.
func (r *Run) Apply(ev Event) error {
	if err := r.commit(ev, true); err != nil {
		return err
	}
	return r.checkpoint()
}

func (r *Run) commit(ev Event, external bool) error {
	if ev == nil {
		return errors.New("workflow/exec: nil event")
	}
	if external && ev.Kind().derived() {
		return fmt.Errorf("workflow/exec: %s is derived by the reducer; submit the fact that caused it", ev.Kind())
	}
	if r.finished {
		return fmt.Errorf("workflow/exec: run %s finished with status %s; refusing %s", r.runID, r.status, ev.Kind())
	}
	if ev.Kind() == EventRunFinished {
		e := ev.(RunFinishedEvent)
		// The run-level linearity guard: a run may not report success while a
		// proposal it sealed is still undisposed. The check is made here, on
		// the close, and not only in the driver's disposition, so it holds
		// whoever submitted the event and whatever order the events came in.
		if e.Status == RunSucceeded && !r.cfg.OpenProposals && len(r.open) > 0 {
			e = RunFinishedEvent{
				Status: RunFailed,
				Reason: fmt.Sprintf("%s: %d proposal(s) left without a disposition (%s); declare the disposition with a settle node",
					ReasonOpenProposalsAtExit, len(r.open), r.openProposalDetail()),
			}
			ev = e
		}
		// Everything that has not terminated is terminated by the close, so
		// the log cannot claim a finished run with live nodes in it. The close
		// is the run's own answer: mark it, so an item terminating inside the
		// scan cannot close its fan-out as succeeded on the way out.
		r.closing = true
		if err := r.cancelOpen(e.Reason); err != nil {
			return err
		}
		r.status = e.Status
		r.reason = e.Reason
		r.finished = true
		r.finishedAt = r.rt.Now()
		r.seq++
		r.events = append(r.events, describe(r.seq, ev))
		return nil
	}
	r.seq++
	r.events = append(r.events, describe(r.seq, ev))

	switch e := ev.(type) {
	case RunStartedEvent:
		return r.onRunStarted(e)
	case RunResumedEvent:
		return r.onRunResumed(e)
	case NodeReadyEvent:
		return r.onNodeReady(e)
	case DispatchStartedEvent:
		return r.onDispatchStarted(e)
	case AttemptCompletedEvent:
		return r.onAttemptCompleted(e)
	case RetryScheduledEvent:
		return r.onRetryScheduled(e)
	case NodeSucceededEvent:
		return r.onTerminal(e.NodePath, StateSucceeded, "", ev)
	case NodeFailedEvent:
		return r.onTerminal(e.NodePath, StateFailed, e.Reason, ev)
	case NodeSkippedEvent:
		return r.onTerminal(e.NodePath, StateSkipped, e.Reason, ev)
	case NodeCancelledEvent:
		return r.onNodeCancelled(e)
	case SettlementPlannedEvent:
		return r.onSettlementPlanned(e)
	case SettlementAskedEvent:
		return r.onSettlementAsked(e)
	case ProposalSettledEvent:
		return r.onProposalSettled(e)
	case FanoutExpandedEvent:
		return r.onFanoutExpanded(e)
	case LoopIterationStartedEvent:
		return r.onLoopIterationStarted(e)
	case LoopCompletedEvent:
		return r.onLoopCompleted(e)
	case BranchRoutedEvent:
		return r.onBranchRouted(e)
	case AggregateInputsEvent:
		return r.onAggregateInputs(e)
	case BudgetExceededEvent:
		r.stopping = true
		r.status = RunBudgetExceeded
		r.reason = fmt.Sprintf("budget exceeded: %s=%s", e.Axis, e.Limit)
		return nil
	}
	return fmt.Errorf("workflow/exec: unhandled event %T", ev)
}

func (r *Run) derive(ev Event) error { return r.commit(ev, false) }

func (r *Run) onRunStarted(e RunStartedEvent) error {
	if r.started {
		return errors.New("workflow/exec: run_started applied twice")
	}
	r.started = true
	if e.RunID != "" {
		r.runID = e.RunID
	}
	// Elapsed time and the log's first line read the same clock sample.
	r.startedAt = r.rt.Now()
	r.status = RunRunning
	for _, p := range r.paths {
		if len(r.byPath[p].Needs) == 0 {
			if err := r.derive(NodeReadyEvent{NodePath: p}); err != nil {
				return err
			}
		}
	}
	return nil
}

// onNodeReady is the one place a node enters the ready queue, and it runs the
// propagation, guard, and kind rules immediately, because "ready" without an
// answer about what the node may do is not a state a driver can use.
func (r *Run) onNodeReady(e NodeReadyEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: node_ready for unknown node %q", e.NodePath)
	}
	if st.State.Terminal() {
		return fmt.Errorf("workflow/exec: node %s is %s; cannot become ready", e.NodePath, st.State)
	}
	if st.State == StateRunning {
		// A retried attempt: the failure closed it, the queue reopens it, and
		// every rule that admitted the first attempt admits this one.
		r.retire(e.NodePath)
	}
	st.State = StateReady
	r.insertReady(e.NodePath)
	return r.settle(r.byPath[e.NodePath])
}

// settle applies the three rules that decide whether a ready node is
// dispatched, in the order that keeps the reason truthful: an input that never
// arrived outranks an author's guard, and a guard outranks a kind the core
// cannot execute — the guard was the author's intent, and running the refusal
// message past it would report a problem the run never had to face.
func (r *Run) settle(n *ir.Node) error {
	st := r.state[n.Path]
	// A need that names no step can never terminate. reach.go already reports
	// the node behind it as WF004; the scheduler leaves it pending and the run
	// finalizes it as cancelled. A body instance resolves its needs inside its
	// own item first, which is why the resolution is a call and not a map read.
	if needs, ok := r.needPaths(n); ok {
		for _, dep := range needs {
			switch r.state[dep].State {
			case StateSkipped:
				return r.derive(NodeSkippedEvent{
					NodePath: n.Path,
					Reason:   ReasonUpstreamSkipped + ": " + dep,
				})
			case StateCancelled:
				return r.derive(NodeCancelledEvent{
					NodePath: n.Path,
					Reason:   ReasonUpstreamCancelled + ": " + dep,
				})
			case StateFailed:
				// on_failure: continue on the need is the declaration that the
				// run proceeds past it. The failure is recorded as an exposed
				// input so a trace and a later bead see what the node was handed.
				st.Exposed = append(st.Exposed, dep)
			}
		}
	}
	// Routing precedes the guard for the same reason an upstream skip does: an arm
	// the deciding branch did not select is a node this path through the graph
	// never reached, so running the author's guard against it would report a
	// problem the run never had to face. An arm whose branch has not terminated yet
	// is a WAIT — the driver holds it instead of starting it — so nothing is decided
	// here and the question is asked again when the branch terminates.
	if selected, undecided := r.armRouting(n.Path); undecided {
		return nil
	} else if !selected {
		return r.derive(NodeSkippedEvent{NodePath: n.Path, Reason: r.armSkipReason(n.Path)})
	}
	if n.When != nil && r.cfg.Guard != nil {
		ok, err := r.cfg.Guard(n, r.snapshot(n.Path, len(st.Attempts)+1))
		if err != nil {
			return r.failTerminal(n.Path, ReasonGuardError+": "+err.Error())
		}
		if !ok {
			return r.derive(NodeSkippedEvent{NodePath: n.Path, Reason: ReasonGuardFalse})
		}
	}
	switch action, note := classifyKind(n); action {
	case kindRefuse:
		return r.failTerminal(n.Path, note)
	case kindFanoutStep:
		// A fan-out node stays ready: it dispatches nothing itself, and its
		// items become schedulable state when the driver reports the resolved
		// `over` list through fanout_expanded. Returning here is what keeps the
		// expansion inside Apply with every other state change, and it is also
		// why a fan-out whose producer has not terminated costs the run nothing
		// while it waits.
		return nil
	case kindLoopStep, kindBranchStep, kindAggregateStep:
		// A loop, a branch, and an aggregate stay ready: none of them dispatches
		// anything itself. The driver resolves what each needs from run state —
		// one loop iteration past the last, one routed value, one resolved input
		// list — and the body instances they create are the dispatches, counted
		// against the global cap and the budget axes like any other node's. What
		// they cannot do at all was already refused by classifyKind, before a
		// single instance existed.
		return nil
	case kindSettleStep:
		// A settle node stays ready. Its handles may name a producer that has
		// not terminated yet, and "not yet" is not "nothing": the driver
		// reports the resolved plan through settlement_planned once the
		// ledger can answer for it. Nothing is dispatched and no attempt is
		// started, so a settlement costs no dispatch budget and holds no slot
		// under MaxConcurrentDispatches.
		return nil
	case kindValueStep:
		// An emit settles here: it renders its compiled value against the
		// run state and records the binding, so it never reaches the door
		// and never consumes a dispatch.
		return r.settleValueStep(n, st)
	}
	return nil
}

func (r *Run) onDispatchStarted(e DispatchStartedEvent) error {
	a := e.Attempt
	if a == nil {
		return errors.New("workflow/exec: dispatch_started with no attempt")
	}
	st := r.state[a.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: dispatch_started for unknown node %q", a.NodePath)
	}
	if st.State != StateReady {
		return fmt.Errorf("workflow/exec: node %s is %s; cannot start an attempt", a.NodePath, st.State)
	}
	// The cap is an invariant of the reducer, not a courtesy of the driver: a
	// driver that asked for one attempt too many gets an error, and no state
	// changes.
	if cap := r.cfg.Limits.MaxConcurrentDispatches; cap > 0 && r.inFlight+1 > cap {
		return fmt.Errorf("workflow/exec: starting %s would exceed max_concurrent_dispatches=%d", a.DispatchID, cap)
	}
	// The nested cap is the same kind of invariant for a fan-out's items: the
	// container's own max_parallel bounds its in-flight dispatches whether or
	// not the global cap has room.
	if inst := r.instances[a.NodePath]; inst != nil {
		if max := r.itemMaxParallel(a.NodePath); max > 0 && r.itemInFlight[inst.owner]+1 > max {
			return fmt.Errorf("workflow/exec: starting %s would exceed %s max_parallel=%d",
				a.DispatchID, inst.owner, max)
		}
		r.itemInFlight[inst.owner]++
	}
	if a.StartedAt.IsZero() {
		a.StartedAt = r.rt.Now()
	}
	st.State = StateRunning
	r.dropReady(a.NodePath)
	st.Attempts = append(st.Attempts, a)
	r.current[a.NodePath] = a
	delete(r.backoff, a.NodePath)
	r.dispatches++
	r.inFlight++
	if r.inFlight > r.peakInFlight {
		r.peakInFlight = r.inFlight
	}
	return nil
}

func (r *Run) onAttemptCompleted(e AttemptCompletedEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: attempt_completed for unknown node %q", e.NodePath)
	}
	if e.RuntimeError != "" {
		if st.State == StateReady {
			// Start itself failed: nothing was dispatched, so there is no
			// attempt to record and no envelope to read. A broken door is not
			// a retryable dispatch — retrying through it would loop.
			r.retire(e.NodePath)
			return r.failTerminal(e.NodePath, ReasonRuntimeError+": "+e.RuntimeError)
		}
		if st.State == StateRunning {
			// The attempt intent was persisted before the door was asked, and
			// the door refused (backoff or start). Close the recorded attempt
			// as a terminal runtime error; no envelope ever came back, so there
			// is no completion to fold.
			r.retire(e.NodePath)
			return r.failTerminal(e.NodePath, ReasonRuntimeError+": "+e.RuntimeError)
		}
	}
	if st.State != StateRunning {
		return fmt.Errorf("workflow/exec: attempt_completed for node %s in state %s", e.NodePath, st.State)
	}
	a := r.current[e.NodePath]
	if a == nil || a.DispatchID != e.DispatchID || a.Number != e.Attempt {
		return fmt.Errorf("workflow/exec: attempt_completed %s#%d does not match the running attempt of node %s",
			e.NodePath, e.Attempt, e.DispatchID)
	}
	if e.RuntimeError != "" {
		r.finishAttempt(a, e)
		r.retire(e.NodePath)
		return r.failTerminal(e.NodePath, ReasonRuntimeError+": "+e.RuntimeError)
	}
	c := e.Completion
	r.finishAttempt(a, e)
	r.retire(e.NodePath)

	if c.Completed() {
		return r.derive(NodeSucceededEvent{NodePath: e.NodePath, Outputs: outputNames(r.state[e.NodePath])})
	}
	st.Failures++
	// Retry only on the envelope's own assertion, and only inside the ceiling
	// the author declared. Error prose is never re-guessed: a lane that wants
	// a retry has to say so in recovery.retryable.
	if c.Retryable() && len(st.Attempts) < st.MaxAttempts {
		if err := r.derive(RetryScheduledEvent{
			NodePath: e.NodePath,
			Attempt:  len(st.Attempts) + 1,
			Backoff:  backoffFor(r.byPath[e.NodePath]),
		}); err != nil {
			return err
		}
		return r.derive(NodeReadyEvent{NodePath: e.NodePath})
	}
	return r.failTerminal(e.NodePath, failureReason(st.MaxAttempts, c))
}

// failureReason states why a node is done failing, from the envelope's typed
// fields. It names the last stop reason and the ceiling that closed the
// attempts, so an author can tell "not worth retrying" from "out of attempts"
// without reading the reducer.
func failureReason(maxAttempts int, c Completion) string {
	reason := ReasonDispatchFailed + ": status=" + c.Status
	if c.StopReason != "" {
		reason += " stop_reason=" + c.StopReason
	}
	if c.Error != "" {
		reason += " error=" + c.Error
	}
	switch {
	case c.Recovery == nil:
		reason += " (envelope asserted no recovery block)"
	case c.Retryable():
		reason += fmt.Sprintf(" (attempt ceiling %d reached)", maxAttempts)
	}
	return reason
}

func (r *Run) onRetryScheduled(e RetryScheduledEvent) error {
	if r.state[e.NodePath] == nil {
		return fmt.Errorf("workflow/exec: retry_scheduled for unknown node %q", e.NodePath)
	}
	if e.Backoff > 0 {
		r.backoff[e.NodePath] = e.Backoff
	}
	return nil
}

// onTerminal closes a node and releases its dependents. The running tally is
// retired first, so a node that ends while an attempt is nominally open cannot
// leave the cap counting a dispatch that no longer exists.
func (r *Run) onTerminal(path string, to State, reason string, ev Event) error {
	st := r.state[path]
	if st == nil {
		return fmt.Errorf("workflow/exec: terminal event for unknown node %q", path)
	}
	if st.State.Terminal() {
		return fmt.Errorf("workflow/exec: node %s is already %s", path, st.State)
	}
	r.retire(path)
	r.dropReady(path)
	r.releaseClaims(path)
	st.State = to
	st.Reason = reason
	switch e := ev.(type) {
	case NodeFailedEvent:
		st.Retryable = e.Retryable
	}
	if err := r.release(path); err != nil {
		return err
	}
	// The node's dependents were released first, so a fan-out that closes here
	// closes onto an already-propagated graph rather than racing its own
	// downstream nodes.
	return r.noteItemOutcome(path)
}

func (r *Run) onNodeCancelled(e NodeCancelledEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: node_cancelled for unknown node %q", e.NodePath)
	}
	if st.State.Terminal() {
		return fmt.Errorf("workflow/exec: node %s is already %s", e.NodePath, st.State)
	}
	if e.CancelErr != "" {
		st.CancelErr = e.CancelErr
	}
	if a := r.current[e.NodePath]; a != nil {
		a.Cancelled = true
	}
	r.retire(e.NodePath)
	r.dropReady(e.NodePath)
	r.releaseClaims(e.NodePath)
	st.State = StateCancelled
	st.Reason = e.Reason
	// A cancelled need still terminates: join-all releases the dependent, and
	// the dependent's own propagation rule cancels it. That is how one cancel
	// walks the graph instead of leaving a pending island.
	if err := r.release(e.NodePath); err != nil {
		return err
	}
	return r.noteItemOutcome(e.NodePath)
}

// failTerminal applies the node's own on_failure disposition to a failure that
// will not be retried.
//
// A fan-out ITEM is the exception, and deliberately so: an item's failure
// disposition belongs to the container's on_item_failure, never to the body
// node's own on_failure, because the item is one of N and whether N-1 siblings
// still count is the container's declaration. So an instance's failure is
// recorded without stopping the run, and noteItemOutcome decides what it means
// for the fan-out. An author who wrote skip on the body node still gets the
// skip: that is a statement about the node, not about the container.
func (r *Run) failTerminal(path, reason string) error {
	st := r.state[path]
	switch st.Policy {
	case ir.OnFailureSkip:
		return r.derive(NodeSkippedEvent{NodePath: path, Reason: reason})
	case ir.OnFailureContinue:
		return r.derive(NodeFailedEvent{NodePath: path, Reason: reason})
	default:
		if err := r.derive(NodeFailedEvent{NodePath: path, Reason: reason}); err != nil {
			return err
		}
		if r.instances[path] != nil {
			return nil
		}
		r.stopping = true
		r.status = RunAborted
		r.reason = path + ": on_failure: abort — " + reason
		return nil
	}
}

// release applies join-all: a dependent becomes ready when every need it
// declares has terminated. Dependents are visited in canonical path order so
// the derived events do not depend on map order or on which need finished last.
func (r *Run) release(path string) error {
	for _, dep := range r.dependents[path] {
		if r.state[dep].State != StatePending {
			continue
		}
		if !r.allNeedsTerminal(dep) {
			continue
		}
		if err := r.derive(NodeReadyEvent{NodePath: dep}); err != nil {
			return err
		}
	}
	return nil
}

// allNeedsTerminal reports whether every need of one node has terminated.
func (r *Run) allNeedsTerminal(path string) bool {
	needs, ok := r.needPaths(r.byPath[path])
	if !ok {
		return false
	}
	for _, dep := range needs {
		st := r.state[dep]
		if st == nil || !st.State.Terminal() {
			return false
		}
	}
	return true
}

// needPaths resolves a node's declared needs to the paths that must terminate
// first. A need names a step id in the node's own scope: a top-level node's
// needs name top-level ids, and a fan-out instance's needs name its item's
// body-local ids FIRST and top-level ids second, which is the shadowing rule
// ir.resolveBody's child scope applies at compile time. ok is false when a need
// resolves to nothing, which strands the node exactly as it always has.
func (r *Run) needPaths(n *ir.Node) ([]string, bool) {
	if n == nil || len(n.Needs) == 0 {
		return nil, true
	}
	inst := r.instances[n.Path]
	out := make([]string, 0, len(n.Needs))
	for _, ref := range n.Needs {
		if inst != nil {
			if p, ok := inst.locals[ref]; ok {
				out = append(out, p)
				continue
			}
		}
		dep, ok := r.byID[ref]
		if !ok {
			return nil, false
		}
		out = append(out, dep.Path)
	}
	return out, true
}

// retire frees the node's in-flight slot. It is idempotent because a terminal
// transition and a retry both retire, and neither should be able to double
// decrement the tally the cap is measured against. The authoritative mark is
// r.current: the first retire deletes the attempt, so every later retire for
// the same node — onTerminal after onAttemptCompleted, a retry reopen after a
// failure close — is a no-op. (A State-based guard cannot serve here: retire
// runs mid-transition, before the terminal state lands, so two retires in one
// completion chain would both see StateRunning and both decrement.)
func (r *Run) retire(path string) {
	if _, ok := r.current[path]; !ok {
		return
	}
	r.inFlight--
	delete(r.current, path)
	if inst := r.instances[path]; inst != nil {
		if n := r.itemInFlight[inst.owner]; n > 1 {
			r.itemInFlight[inst.owner] = n - 1
		} else {
			delete(r.itemInFlight, inst.owner)
		}
	}
}

func (r *Run) finishAttempt(a *Attempt, e AttemptCompletedEvent) {
	c := e.Completion
	st := r.state[a.NodePath]
	a.FinishedAt = r.rt.Now()
	a.Status = c.Status
	a.StopReason = c.StopReason
	a.DurationMs = c.DurationMs
	a.Model = c.Model
	a.World = c.World
	a.ProposalID = c.ProposalID
	a.Result = c.Result
	a.Error = c.Error
	a.Partial = c.Partial
	a.RecoveryAsserted = c.Recovery != nil
	a.Retryable = c.Retryable()
	st.Status = c.Status
	st.StopReason = c.StopReason
	st.DurationMs = c.DurationMs
	st.Model = c.Model
	st.World = c.World
	st.ProposalID = c.ProposalID
	st.Result = c.Result
	st.Error = c.Error
	st.Retryable = c.Retryable()
	st.RecoverySet = c.Recovery != nil
	st.Contract = c.ContractStatus()
	// A delegate's declared output binding is the delivered result text, and
	// its contract verdict is whatever the envelope asserted. Extracting a
	// field out of a structured Report is value plumbing (yaah-0u0v.13);
	// nothing here re-validates the result against the declared type or
	// schema — that judgement belongs to the response-contract machinery on
	// the lane (yaah-was9.2), whose verdict is mirrored verbatim below.
	if n := r.byPath[a.NodePath]; n != nil && n.Output != nil && n.Output.Name != "" {
		r.recordOutput(st, NodeOutput{
			NodePath: n.Path,
			Kind:     n.Kind,
			Name:     n.Output.Name,
			Value:    c.Result,
			Contract: c.ContractStatus(),
		})
	}
	// A proposal a node sealed stays open until a settle node disposes of it.
	// The scheduler records the typed handle, adds the id to the run's open
	// ledger, and disposes of nothing here — which is what keeps WF021's
	// "settled exactly once" claim a fact about the run rather than an
	// assumption about the lane.
	if c.ProposalID != "" {
		st.Proposal = &ProposalHandle{
			ProposalID: c.ProposalID,
			Producer:   st.Path,
			Role:       c.Role,
			Lane:       c.Lane,
			DispatchID: a.DispatchID,
		}
		r.open[c.ProposalID] = st.Path
	}
}

func (r *Run) insertReady(path string) {
	i := sort.SearchStrings(r.ready, path)
	if i < len(r.ready) && r.ready[i] == path {
		return
	}
	r.ready = append(r.ready, "")
	copy(r.ready[i+1:], r.ready[i:])
	r.ready[i] = path
}

func (r *Run) dropReady(path string) {
	i := sort.SearchStrings(r.ready, path)
	if i < len(r.ready) && r.ready[i] == path {
		r.ready = append(r.ready[:i], r.ready[i+1:]...)
	}
}

// cancelOpen terminates every node that has not, in canonical order, when the
// run closes. The reason is carried onto each node so a trace can tell a node
// the author skipped from a node the run never got to. Fan-out instances are in
// the scan: an instance left pending would otherwise outlive the run in the
// result with no disposition at all.
func (r *Run) cancelOpen(reason string) error {
	for _, p := range r.allPaths() {
		st := r.state[p]
		if st.State.Terminal() {
			continue
		}
		if err := r.derive(NodeCancelledEvent{
			NodePath: p,
			Reason:   ReasonRunStopped + ": " + reason,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Result is the run's outcome, its canonical event log, and a snapshot of
// every node's state. Completed attempts are immutable, so the snapshot shares
// them rather than copying bytes a trace would have to re-verify.
func (r *Run) Result() Result {
	out := Result{
		RunID:        r.runID,
		Status:       r.status,
		Reason:       r.reason,
		Dispatches:   r.dispatches,
		PeakInFlight: r.peakInFlight,
		Events:       append([]string(nil), r.events...),
		Warnings:     append([]string(nil), r.warnings...),
	}
	if !r.startedAt.IsZero() {
		end := r.finishedAt
		if end.IsZero() {
			end = r.rt.Now()
		}
		out.Elapsed = end.Sub(r.startedAt)
	}
	for _, p := range r.allPaths() {
		st := *r.state[p]
		st.Attempts = append([]*Attempt(nil), r.state[p].Attempts...)
		st.Outputs = append([]NodeOutput(nil), r.state[p].Outputs...)
		st.Items = append([]FanoutItem(nil), r.state[p].Items...)
		st.Iterations = append([]LoopIteration(nil), r.state[p].Iterations...)
		out.Nodes = append(out.Nodes, &st)
		// Run-level outputs are every node's bindings in canonical node-path
		// order, so the list is reproducible and needs no map to read.
		out.Outputs = append(out.Outputs, st.Outputs...)
	}
	return out
}

// Node returns one node's current state.
func (r *Run) Node(path string) (NodeState, bool) {
	st, ok := r.state[path]
	if !ok {
		return NodeState{}, false
	}
	return *st, true
}

// Events returns the canonical log so far.
func (r *Run) Events() []string { return append([]string(nil), r.events...) }

// Status reports the run's disposition, terminal or not.
func (r *Run) Status() RunStatus { return r.status }

// Result is what a finished run says about itself.
type Result struct {
	RunID  string
	Status RunStatus
	// Reason names the stopping cause: the aborting node, the exhausted
	// budget axis, or the first node that ended failed and was exposed.
	Reason     string
	Dispatches int
	// PeakInFlight is the highest number of attempts the reducer had open at
	// once — the scheduler's own witness that MaxConcurrentDispatches held.
	PeakInFlight int
	Elapsed      time.Duration
	Events       []string
	Warnings     []string
	Nodes        []*NodeState
	// Outputs flattens every node's recorded output bindings in canonical
	// node-path order: the delegate reports and emit values a run produced.
	Outputs []NodeOutput
}
