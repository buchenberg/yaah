package exec

import (
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// State is one node's position in the lifecycle. The vocabulary is exactly the
// bead's: pending, ready, running, then one of four terminal states.
type State string

// Lifecycle states.
const (
	StatePending   State = "pending"
	StateReady     State = "ready"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateSkipped   State = "skipped"
	StateCancelled State = "cancelled"
)

// Terminal reports whether a state can change again. Terminal is precisely the
// set that satisfies a join-all need: a node in one of these states has
// terminated, which is what releases its dependents. Ready, running, and
// pending are not, and a readiness rule that counted them would either hang or
// run a node against an input that does not exist yet.
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateSkipped, StateCancelled:
		return true
	}
	return false
}

// RunStatus is the disposition of a finished run.
type RunStatus string

// Run dispositions.
const (
	// RunSucceeded: every node terminated succeeded or skipped. A skip is a
	// completed disposition, not a failure — the author declared it.
	RunSucceeded RunStatus = "succeeded"
	// RunFailed: the run drained to completion but a node ended failed under
	// on_failure: continue, so the failure was exposed downstream instead of
	// stopping the run.
	RunFailed RunStatus = "failed"
	// RunAborted: a node ended failed under on_failure: abort (the default),
	// and the run stopped scheduling.
	RunAborted RunStatus = "aborted"
	// RunCancelled: the run was cancelled from outside, or its context ended.
	RunCancelled RunStatus = "cancelled"
	// RunBudgetExceeded: a declared budget was exhausted. The run stops with a
	// status rather than hanging or spinning.
	RunBudgetExceeded RunStatus = "budget_exceeded"
	// RunRunning is the pre-terminal status.
	RunRunning RunStatus = "running"
)

// Attempt is one dispatched try of one node. Every envelope fact the completion
// carried is recorded here as it arrived, because trace records
// (yaah-0u0v.17) read attempt history and must not have to re-derive a
// duration, a model, or a world from per-lane plumbing.
type Attempt struct {
	// Number is the 1-based per-node attempt ordinal.
	Number int
	// NodePath and DispatchID identify the attempt absolutely; DispatchID is
	// "<node path>#<number>", which is the shape the dispatch envelope's
	// dispatch_id already uses.
	NodePath   string
	DispatchID string
	// Role and Lane echo the node's declared dispatch identity for the trace
	// surface; they are facts of the graph, not of the envelope.
	Role string
	Lane string
	// Backoff is the virtual or real delay the run waited before starting
	// this attempt, honored through Runtime.Backoff.
	Backoff time.Duration
	// Handle is the door's reference to the started attempt.
	Handle Handle

	StartedAt  time.Time
	FinishedAt time.Time

	// Status is the envelope's status: proposal, report, shared, completed,
	// or error.
	Status string
	// StopReason is the envelope's typed stop reason. It is recorded verbatim;
	// the scheduler never invents one and never infers one from error text.
	StopReason string
	// DurationMs is the envelope's wall-clock span of the dispatch.
	DurationMs int64
	Model      string
	World      string
	ProposalID string
	Result     string
	Error      string
	// Partial mirrors the envelope's flag that Result is what the run produced
	// before it stopped.
	Partial bool
	// Retryable is the envelope's recovery.retryable, verbatim.
	Retryable bool
	// RecoveryAsserted records whether the envelope carried a recovery block
	// at all. Absent is a third value distinct from "retryable: false", and
	// the scheduler treats it as no licence to retry.
	RecoveryAsserted bool
	// Cancelled marks an attempt the run stopped through the door. Its
	// envelope, if it ever arrives, is dropped: the reducer's state is
	// authoritative.
	Cancelled bool
}

// Duration is the attempt's reported wall-clock span.
func (a *Attempt) Duration() time.Duration {
	if a == nil {
		return 0
	}
	return time.Duration(a.DurationMs) * time.Millisecond
}

// NodeState is one node's lifecycle state, owned by the reducer.
type NodeState struct {
	Path string
	ID   string
	Kind ir.Kind
	// State is the lifecycle position.
	State State
	// Needs is the node's declared needs, in author order.
	Needs []string
	// Policy is the effective failure disposition, with the folded default
	// applied: an undeclared on_failure is abort.
	Policy ir.FailurePolicy
	// MaxAttempts is the attempt ceiling from the node's retry block, at
	// least 1.
	MaxAttempts int
	// Attempts is the attempt history in start order.
	Attempts []*Attempt
	// Failures counts attempts that came back not-completed.
	Failures int
	// Reason is the terminal reason for a failed, skipped, or cancelled node.
	Reason string
	// Exposed names the need paths that ended failed under on_failure:
	// continue and were therefore handed to this node as an input fact.
	Exposed []string
	// CancelErr records a Runtime.Cancel door error for this node's attempt.
	// A cancel that failed is a fact about the world, not a state change, so
	// it is kept next to the state the run already committed.
	CancelErr string

	// The fields below carry the last completion's envelope facts for
	// consumers that only want the node's outcome. Attempt history holds the
	// full sequence.
	Status       string
	StopReason   string
	DurationMs   int64
	Model        string
	World        string
	ProposalID   string
	Result       string
	Error        string
	Retryable    bool
	RecoverySet  bool
	OutputHandle any
}

// LastAttempt returns the node's most recent attempt, or nil.
func (n *NodeState) LastAttempt() *Attempt {
	if n == nil || len(n.Attempts) == 0 {
		return nil
	}
	return n.Attempts[len(n.Attempts)-1]
}
