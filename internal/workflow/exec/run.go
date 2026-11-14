package exec

import (
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

	nodes      []*ir.Node
	paths      []string // every top-level node path, sorted: the canonical order
	byPath     map[string]*ir.Node
	byID       map[string]*ir.Node
	state      map[string]*NodeState
	dependents map[string][]string // step id -> paths of the steps that need it
	params     map[string]any

	ready   []string // node paths in canonical order; the launch order
	current map[string]*Attempt
	backoff map[string]time.Duration

	// outstanding is the driver's queue of started, uncommitted attempts in
	// dispatch order. It is door bookkeeping, not lifecycle state: no
	// scheduler decision reads it except "is there anything to wait for".
	outstanding []*Attempt

	events   []string
	warnings []string
	seq      int
	started  bool
	finished bool

	dispatches   int
	inFlight     int
	peakInFlight int
	startedAt    time.Time
	finishedAt   time.Time
	status       RunStatus
	reason       string
	stopping     bool
}

// New prepares a run over a resolved graph. Only the graph's top-level steps
// are scheduled here: a body node is entered through its container, and the
// containers are .14/.15, so a body node is not lifecycle state of this run
// and is not silently skipped either — the container that owns it refuses.
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
		cfg:        cfg,
		rt:         rt,
		graph:      g,
		params:     params,
		byPath:     map[string]*ir.Node{},
		byID:       map[string]*ir.Node{},
		state:      map[string]*NodeState{},
		dependents: map[string][]string{},
		current:    map[string]*Attempt{},
		backoff:    map[string]time.Duration{},
		status:     RunRunning,
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
		for _, ref := range n.Needs {
			r.dependents[ref] = append(r.dependents[ref], n.Path)
		}
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
func (r *Run) Apply(ev Event) error {
	return r.commit(ev, true)
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
		// Everything that has not terminated is terminated by the close, so
		// the log cannot claim a finished run with live nodes in it.
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
	for _, ref := range n.Needs {
		dep, ok := r.byID[ref]
		if !ok {
			// A need that names no step can never terminate. reach.go already
			// reports the node behind it as WF004; the scheduler leaves it
			// pending and the run finalizes it as cancelled.
			continue
		}
		switch r.state[dep.Path].State {
		case StateSkipped:
			return r.derive(NodeSkippedEvent{
				NodePath: n.Path,
				Reason:   ReasonUpstreamSkipped + ": " + dep.Path,
			})
		case StateCancelled:
			return r.derive(NodeCancelledEvent{
				NodePath: n.Path,
				Reason:   ReasonUpstreamCancelled + ": " + dep.Path,
			})
		case StateFailed:
			// on_failure: continue on the need is the declaration that the
			// run proceeds past it. The failure is recorded as an exposed
			// input so a trace and a later bead see what the node was handed.
			st.Exposed = append(st.Exposed, dep.Path)
		}
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
	st.State = to
	st.Reason = reason
	switch e := ev.(type) {
	case NodeFailedEvent:
		st.Retryable = e.Retryable
	}
	return r.release(path)
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
	st.State = StateCancelled
	st.Reason = e.Reason
	// A cancelled need still terminates: join-all releases the dependent, and
	// the dependent's own propagation rule cancels it. That is how one cancel
	// walks the graph instead of leaving a pending island.
	return r.release(e.NodePath)
}

// failTerminal applies the node's own on_failure disposition to a failure that
// will not be retried.
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
	id := r.byPath[path].ID
	if id == "" {
		return nil
	}
	for _, dep := range r.dependents[id] {
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

func (r *Run) allNeedsTerminal(path string) bool {
	for _, ref := range r.byPath[path].Needs {
		dep, ok := r.byID[ref]
		if !ok {
			return false
		}
		if !r.state[dep.Path].State.Terminal() {
			return false
		}
	}
	return true
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
	// A proposal a node sealed stays open: the scheduler records the id and
	// disposes of nothing, which is what leaves room for the settlement verbs
	// (.13) and keeps WF021's "settled exactly once" claim true.
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
// the author skipped from a node the run never got to.
func (r *Run) cancelOpen(reason string) error {
	for _, p := range r.paths {
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
	for _, p := range r.paths {
		st := *r.state[p]
		st.Attempts = append([]*Attempt(nil), r.state[p].Attempts...)
		st.Outputs = append([]NodeOutput(nil), r.state[p].Outputs...)
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
