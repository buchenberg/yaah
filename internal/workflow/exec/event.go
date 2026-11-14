package exec

import (
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// EventKind names one lifecycle fact. The vocabulary is fixed: a scheduler
// that needs a new word has found a new state, and states are the contract
// the trace surface, the persistence bead, and the CLI read.
type EventKind string

// Event kinds.
const (
	EventRunStarted       EventKind = "run_started"
	EventNodeReady        EventKind = "node_ready"
	EventDispatchStarted  EventKind = "dispatch_started"
	EventAttemptCompleted EventKind = "attempt_completed"
	EventRetryScheduled   EventKind = "retry_scheduled"
	EventNodeSucceeded    EventKind = "node_succeeded"
	EventNodeFailed       EventKind = "node_failed"
	EventNodeSkipped      EventKind = "node_skipped"
	EventNodeCancelled    EventKind = "node_cancelled"
	EventBudgetExceeded   EventKind = "budget_exceeded"
	EventRunFinished      EventKind = "run_finished"
	// EventSettlementPlanned reports the handle set one settle node resolved
	// from recorded run state; EventProposalSettled reports one disposition the
	// door's settlement path returned. Both are inputs: the driver reads the
	// graph's own facts and the world's own answers, and the reducer decides
	// what either means for the node and the run.
	EventSettlementPlanned EventKind = "settlement_planned"
	EventProposalSettled   EventKind = "proposal_settled"
	// EventFanoutExpanded reports the item list one fan-out node resolved from
	// its declared `over` source (yaah-0u0v.14). It is the fan-out's answer to
	// the same question a settlement plan answers — what work does this node own
	// — and like the plan it is an input: the reducer instantiates the item
	// instances, and owns everything that follows.
	EventFanoutExpanded EventKind = "fanout_expanded"
	// EventLoopIterationStarted asks the reducer to instantiate one loop
	// iteration's body (yaah-0u0v.15); EventLoopCompleted reports what ended the
	// loop — its declared terminator, its declared bound, or a terminator the run
	// could not evaluate. Both are inputs: the driver decides WHEN, the reducer
	// decides what it means, and the instances a loop creates are ordinary nodes
	// from there on.
	EventLoopIterationStarted EventKind = "loop_iteration_started"
	EventLoopCompleted        EventKind = "loop_completed"
	// EventBranchRouted reports the arm a branch's selector resolved to
	// (yaah-0u0v.15). It is the branch's whole decision as an input, so the
	// reducer applies it — record the arm, skip the rest — inside Apply.
	EventBranchRouted EventKind = "branch_routed"
	// EventAggregateInputs reports the values an aggregate's inputs resolved to
	// (yaah-0u0v.15). A deterministic strategy combines them in the reducer; a
	// summarize instantiates its body.
	EventAggregateInputs EventKind = "aggregate_inputs"
)

// derived reports whether only the reducer may emit a kind. The split is the
// reason Apply can be the sole mutator: the driver reports what the world did
// (a completion, a cancel, a budget breach), and the reducer decides what that
// means for the graph.
func (k EventKind) derived() bool {
	switch k {
	case EventNodeReady, EventRetryScheduled, EventNodeSucceeded, EventNodeFailed, EventNodeSkipped:
		return true
	}
	return false
}

// orDash renders an optional log field: empty becomes "-" so event lines
// never show bare "model=" noise while column positions stay grep-stable.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Event is one fact about a run. The marker method seals the interface: a
// caller outside this package cannot invent an event kind the reducer has
// never heard of, which is what makes the exhaustive switch in Apply total.
type Event interface {
	Kind() EventKind
	target() string
}

// RunStartedEvent opens a run. The reducer takes the run clock here, so
// elapsed time and the event log start from the same instant.
type RunStartedEvent struct{ RunID string }

func (RunStartedEvent) Kind() EventKind  { return EventRunStarted }
func (e RunStartedEvent) target() string { return "" }

// NodeReadyEvent records that a node passed its join-all test and is in the
// ready queue. It is derived: readiness is a consequence of a need
// terminating, never an assertion from outside.
type NodeReadyEvent struct{ NodePath string }

func (NodeReadyEvent) Kind() EventKind  { return EventNodeReady }
func (e NodeReadyEvent) target() string { return e.NodePath }

// DispatchStartedEvent records that the run handed one attempt to the
// Runtime door and the door accepted it. The driver mints the attempt before
// calling Start, because the attempt's dispatch id is part of the request; the
// state transition still happens here.
type DispatchStartedEvent struct{ Attempt *Attempt }

func (DispatchStartedEvent) Kind() EventKind { return EventDispatchStarted }
func (e DispatchStartedEvent) target() string {
	if e.Attempt == nil {
		return ""
	}
	return e.Attempt.NodePath
}

// AttemptCompletedEvent carries one completion envelope back from the door.
// It is the only way a run learns that an attempt's work ended, and the only
// place failure semantics are decided.
type AttemptCompletedEvent struct {
	NodePath   string
	DispatchID string
	Attempt    int
	Completion Completion
	// RuntimeError reports that the door itself malfunctioned — Start failed,
	// or a completion arrived that the reducer cannot act on. There is no
	// envelope behind it, so it asserts nothing about retryability and is
	// terminal.
	RuntimeError string
}

func (AttemptCompletedEvent) Kind() EventKind  { return EventAttemptCompleted }
func (e AttemptCompletedEvent) target() string { return e.NodePath }

// RetryScheduledEvent records that a retryable failure will be retried, and
// how long the run must wait first. The delay is honored through the door
// (Runtime.Backoff), so a test's virtual clock moves without any goroutine
// sleeping.
type RetryScheduledEvent struct {
	NodePath string
	Attempt  int
	Backoff  time.Duration
}

func (RetryScheduledEvent) Kind() EventKind  { return EventRetryScheduled }
func (e RetryScheduledEvent) target() string { return e.NodePath }

// NodeSucceededEvent closes a node that completed its work. Outputs names the
// output bindings the reducer recorded for it — the delegate report or the
// emitted value — so the event log says which node produced what without
// quoting any of the values themselves.
type NodeSucceededEvent struct {
	NodePath string
	Outputs  []string
}

func (NodeSucceededEvent) Kind() EventKind  { return EventNodeSucceeded }
func (e NodeSucceededEvent) target() string { return e.NodePath }

// NodeFailedEvent closes a node whose failure is final for this run: retries
// were exhausted, the envelope asserted no retry, or the kind or the door
// refused the work. Reason always names the cause.
type NodeFailedEvent struct {
	NodePath  string
	Reason    string
	Retryable bool
}

func (NodeFailedEvent) Kind() EventKind  { return EventNodeFailed }
func (e NodeFailedEvent) target() string { return e.NodePath }

// NodeSkippedEvent closes a node that was not run: its own on_failure: skip
// absorbed a failure, a need of it was skipped, or its guard did not pass.
type NodeSkippedEvent struct {
	NodePath string
	Reason   string
}

func (NodeSkippedEvent) Kind() EventKind  { return EventNodeSkipped }
func (e NodeSkippedEvent) target() string { return e.NodePath }

// NodeCancelledEvent closes a running, ready, or pending node because the run
// stopped. CancelErr carries a Runtime.Cancel door error when the attempt may
// still be running in the world; the state transition is the run's own
// decision either way.
type NodeCancelledEvent struct {
	NodePath   string
	DispatchID string
	Reason     string
	CancelErr  string
}

func (NodeCancelledEvent) Kind() EventKind  { return EventNodeCancelled }
func (e NodeCancelledEvent) target() string { return e.NodePath }

// SettlementPlannedEvent reports the proposal handles a settle node resolved
// from recorded run state, before any settlement is asked for. Refusal
// carries a reason the reducer turns into the node's terminal state — a
// handle set this bead cannot resolve, or a proposal another node already
// disposed of — so even a refusal changes state only inside Apply.
type SettlementPlannedEvent struct {
	NodePath string
	Refs     []ProposalRef
	Action   ir.SettleAction
	Policy   ir.SettlePolicy
	Order    ir.SettleOrder
	// OnConflict and Empty are the node's declared dispositions, carried so
	// the reducer's decisions read one event and not the graph behind it.
	OnConflict ir.ConflictPolicy
	Empty      ir.EmptyPolicy
	Refusal    string
}

func (SettlementPlannedEvent) Kind() EventKind  { return EventSettlementPlanned }
func (e SettlementPlannedEvent) target() string { return e.NodePath }

// ProposalSettledEvent carries one settlement's outcome back from the door.
// DoorError reports that the door itself malfunctioned: there is no
// settlement envelope behind it, the proposal keeps whatever state it had, and
// the node ends failed rather than retrying a world effect that may already
// have landed.
type ProposalSettledEvent struct {
	NodePath  string
	Settled   Settled
	DoorError string
	// Refused marks a settlement the scheduler declined to ask for at all —
	// an action outside the four settlement verbs — as distinct from one the
	// door attempted and could not complete.
	Refused string
}

func (ProposalSettledEvent) Kind() EventKind  { return EventProposalSettled }
func (e ProposalSettledEvent) target() string { return e.NodePath }

// FanoutExpandedEvent reports the items a fan-out node resolved from its
// declared `over` source. The reducer instantiates one body instance per item
// and owns what follows: which instance may start, the fan-out's nested
// max_parallel, the join, and the collected value. Refusal carries a reason the
// reducer turns into the node's terminal state — an `over` source this run
// cannot read, or a body with nothing to run — so even a refusal changes state
// only inside Apply.
type FanoutExpandedEvent struct {
	NodePath string
	// Items are the resolved `over` elements in input order. The reducer mints
	// one item per element, in this order, and the collected value is assembled
	// in the same order however the dispatches interleave.
	Items   []any
	Refusal string
}

func (FanoutExpandedEvent) Kind() EventKind  { return EventFanoutExpanded }
func (e FanoutExpandedEvent) target() string { return e.NodePath }

// LoopIterationStartedEvent asks the reducer to instantiate one loop iteration.
// Iteration is the 0-based index the reducer must find next in its own ledger: a
// driver asking for any other index is a bug in this package, not a declaration an
// author can fix.
type LoopIterationStartedEvent struct {
	NodePath  string
	Iteration int
}

func (LoopIterationStartedEvent) Kind() EventKind  { return EventLoopIterationStarted }
func (e LoopIterationStartedEvent) target() string { return e.NodePath }

// LoopCompletedEvent reports what ended a loop. Terminated names the declaration
// that fired — until, or the bound — and Exhausted marks the one case that is not
// a success when a terminator WAS declared: the bound reached without the
// condition ever matching. Refusal carries a terminator the run could not
// evaluate, which the reducer turns into the node's terminal state.
type LoopCompletedEvent struct {
	NodePath string
	// Iterations is how many iterations ran, for the log line: the reducer's own
	// ledger is the authority, and the driver states what it read.
	Iterations int
	Terminated string
	Exhausted  bool
	Refusal    string
}

func (LoopCompletedEvent) Kind() EventKind  { return EventLoopCompleted }
func (e LoopCompletedEvent) target() string { return e.NodePath }

// BranchRoutedEvent reports a branch's decision: the step id of the arm the
// resolved value selected, empty when no case matched and no default was declared,
// and Refusal for a value the run could not route on. Value is the resolved value
// rendered as one log field, so the log line says what was matched as well as what
// was chosen.
type BranchRoutedEvent struct {
	NodePath string
	Value    string
	Selected string
	Refusal  string
}

func (BranchRoutedEvent) Kind() EventKind  { return EventBranchRouted }
func (e BranchRoutedEvent) target() string { return e.NodePath }

// AggregateInputsEvent carries the values one aggregate's inputs resolved to, in
// declaration order. Refusal carries an input the run could not read, which the
// reducer turns into the aggregate's terminal state rather than combining a
// shorter list than the author declared.
type AggregateInputsEvent struct {
	NodePath string
	Inputs   []any
	Refusal  string
}

func (AggregateInputsEvent) Kind() EventKind  { return EventAggregateInputs }
func (e AggregateInputsEvent) target() string { return e.NodePath }

// BudgetExceededEvent records that a declared ceiling was reached. Axis and
// Limit are the same strings Limits documents, so a reader never has to map
// an enum to find out what ran out.
type BudgetExceededEvent struct {
	Axis  string
	Limit string
}

func (BudgetExceededEvent) Kind() EventKind { return EventBudgetExceeded }
func (BudgetExceededEvent) target() string  { return "" }

// RunFinishedEvent closes the run. The reducer derives the cancellation of
// every node that never terminated before recording the line, so run_finished
// is always the last event and the log never claims a finished run with live
// nodes in it.
type RunFinishedEvent struct {
	Status RunStatus
	Reason string
}

func (RunFinishedEvent) Kind() EventKind { return EventRunFinished }
func (RunFinishedEvent) target() string  { return "" }

// describe renders one event as its canonical log line: a monotonic sequence
// number, the kind, and the facts. The log is the byte-identical artifact the
// determinism test compares and the shape trace records (yaah-0u0v.17) are
// built from, so the rendering is a fixed switch, not reflection, and it never
// embeds a map iteration or a timestamp.
func describe(seq int, ev Event) string {
	return fmt.Sprintf("%04d %s %s", seq, ev.Kind(), detail(ev))
}

func detail(ev Event) string {
	switch e := ev.(type) {
	case RunStartedEvent:
		return "run=" + e.RunID
	case NodeReadyEvent:
		return e.NodePath
	case DispatchStartedEvent:
		a := e.Attempt
		if a == nil {
			return "nil attempt"
		}
		return fmt.Sprintf("%s role=%s lane=%s attempt=%d backoff=%s",
			a.DispatchID, a.Role, a.Lane, a.Number, a.Backoff)
	case AttemptCompletedEvent:
		c := e.Completion
		out := fmt.Sprintf("%s attempt=%d status=%s stop_reason=%s duration_ms=%d model=%s world=%s retry_asserted=%t retryable=%t",
			e.DispatchID, e.Attempt, orDash(c.Status), orDash(c.StopReason), c.DurationMs, orDash(c.Model), orDash(c.World), c.Recovery != nil, c.Retryable())
		if c.ProposalID != "" {
			out += " proposal_id=" + c.ProposalID
		}
		if e.RuntimeError != "" {
			out += " runtime_error=" + e.RuntimeError
		}
		return out
	case RetryScheduledEvent:
		return fmt.Sprintf("%s attempt=%d backoff=%s", e.NodePath, e.Attempt, e.Backoff)
	case NodeSucceededEvent:
		out := e.NodePath
		if len(e.Outputs) > 0 {
			out += " outputs=" + strings.Join(e.Outputs, ",")
		}
		return out
	case NodeFailedEvent:
		return fmt.Sprintf("%s retryable=%t reason=%s", e.NodePath, e.Retryable, e.Reason)
	case NodeSkippedEvent:
		return e.NodePath + " reason=" + e.Reason
	case NodeCancelledEvent:
		out := e.NodePath + " reason=" + e.Reason
		if e.CancelErr != "" {
			out += " cancel_error=" + e.CancelErr
		}
		return out
	case SettlementPlannedEvent:
		ids := make([]string, 0, len(e.Refs))
		for _, ref := range e.Refs {
			ids = append(ids, ref.ProposalID)
		}
		out := fmt.Sprintf("%s action=%s policy=%s count=%d proposals=%s",
			e.NodePath, orDash(string(e.Action)), orDash(string(e.Policy)), len(e.Refs), orDash(strings.Join(ids, ",")))
		if e.Refusal != "" {
			out += " refusal=" + flatten(e.Refusal)
		}
		return out
	case ProposalSettledEvent:
		s := e.Settled
		out := fmt.Sprintf("%s proposal_id=%s action=%s status=%s left_open=%t",
			e.NodePath, orDash(s.ProposalID), orDash(s.Action), orDash(s.Status), s.LeftOpen)
		if len(s.Conflicts) > 0 {
			out += " conflicts=" + strings.Join(s.Conflicts, ",")
		}
		if s.Error != "" {
			out += " error=" + flatten(s.Error)
		}
		if e.DoorError != "" {
			out += " door_error=" + flatten(e.DoorError)
		}
		if e.Refused != "" {
			out += " refused=" + flatten(e.Refused)
		}
		return out
	case FanoutExpandedEvent:
		out := fmt.Sprintf("%s count=%d", e.NodePath, len(e.Items))
		if e.Refusal != "" {
			out += " refusal=" + flatten(e.Refusal)
		}
		return out
	case LoopIterationStartedEvent:
		return fmt.Sprintf("%s iteration=%d", e.NodePath, e.Iteration)
	case LoopCompletedEvent:
		out := fmt.Sprintf("%s iterations=%d terminated=%s", e.NodePath, e.Iterations, orDash(e.Terminated))
		if e.Exhausted {
			out += " exhausted=true"
		}
		if e.Refusal != "" {
			out += " refusal=" + flatten(e.Refusal)
		}
		return out
	case BranchRoutedEvent:
		out := fmt.Sprintf("%s route=%s value=%s", e.NodePath, orDash(e.Selected), orDash(flatten(e.Value)))
		if e.Refusal != "" {
			out += " refusal=" + flatten(e.Refusal)
		}
		return out
	case AggregateInputsEvent:
		out := fmt.Sprintf("%s inputs=%d", e.NodePath, len(e.Inputs))
		if e.Refusal != "" {
			out += " refusal=" + flatten(e.Refusal)
		}
		return out
	case BudgetExceededEvent:
		return fmt.Sprintf("axis=%s limit=%s", e.Axis, e.Limit)
	case RunFinishedEvent:
		return fmt.Sprintf("status=%s reason=%s", e.Status, e.Reason)
	}
	return "unknown"
}
