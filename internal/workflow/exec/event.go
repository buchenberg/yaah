package exec

import (
	"fmt"
	"strings"
	"time"
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
	case BudgetExceededEvent:
		return fmt.Sprintf("axis=%s limit=%s", e.Axis, e.Limit)
	case RunFinishedEvent:
		return fmt.Sprintf("status=%s reason=%s", e.Status, e.Reason)
	}
	return "unknown"
}
