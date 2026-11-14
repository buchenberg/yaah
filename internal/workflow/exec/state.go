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
	// Contract is the envelope's `contract.status` verdict, verbatim
	// (yaah-was9.2). Empty means the lane asserted nothing about the result
	// contract — which is what every envelope does today, since the field is
	// reserved. The scheduler never derives a verdict of its own.
	Contract string
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
	Status      string
	StopReason  string
	DurationMs  int64
	Model       string
	World       string
	ProposalID  string
	Result      string
	Error       string
	Retryable   bool
	RecoverySet bool
	// Contract is the last completion's contract verdict, verbatim; empty
	// means the envelope carried no contract block.
	Contract string
	// Outputs are the output bindings this node produced, in declaration
	// order. A re-run of the node replaces the binding of the same name
	// rather than appending, so the list is the node's current outputs, not
	// its history (the history is Attempt.Result).
	Outputs []NodeOutput

	// Proposal is the typed handle this node sealed, when a proposal-lane
	// attempt sealed one. It is what a settle node reads as its input, and it
	// stays set after settlement: the disposition is recorded on the settle
	// node and in the run's ledger, not by erasing the producer's fact.
	Proposal *ProposalHandle

	// SettlePlan, Settling, Settled, and Asked are a settle node's own ledger:
	// the handle set it resolved, how far through it the run is, every
	// disposition the door reported, and the one disposition the run has asked
	// for and not yet had answered. They are lifecycle state, so they are
	// written only inside Apply — the driver reads them to decide which
	// settlement to ask the door for next.
	SettlePlan *SettlePlan
	Settling   int
	Settled    []Settled
	// Asked names the proposal the run has asked the door to dispose of with no
	// recorded disposition yet (yaah-0u0v.16). It is the settlement write-ahead
	// record: it is persisted BEFORE the door is called, so a run resumed from a
	// checkpoint can tell "asked, outcome unknown" from "never asked" and refuse
	// to ask twice. Empty means no disposition is in flight.
	Asked string

	// Expanded, Items, and Collect are a fan-out node's own ledger
	// (yaah-0u0v.14). Expanded records that the items were instantiated, so a
	// second expansion of the same node is a refused event rather than a second
	// set of instances; Items is the per-item record in `over` order, and
	// Collect is the joined value the node reports — the item values in that
	// same input order, never completion order.
	Expanded bool
	Items    []FanoutItem
	Collect  []any

	// Winners is the item set a join:any|quorum container closed on
	// (yaah-0u0v.19): the first success under any, the first N under quorum, in
	// item order. It is the fixed answer to "which items the run collected", so
	// Collect holds exactly these items' values and a result that arrives after
	// the close is recorded on its own item without widening either. Empty for
	// join: all, which never decides on a tally.
	Winners []int
	// Discards is the dispositions the run obtained for proposals a NON-WINNING
	// item of this container sealed (yaah-0u0v.19). Each one went through the
	// door's settlement path with the discard verb — the same path a settle node
	// uses — because work the join cancelled must not leave a proposal (or its
	// worktree) standing. A record whose settlement did not land keeps the
	// proposal in the run's open ledger, so the close states the residue instead
	// of asking a second time.
	Discards []Settled

	// Iterations is a loop node's own ledger (yaah-0u0v.15): one record per
	// iteration, in iteration order, folded from the iteration's instance states
	// as they all terminate. Collect accumulates each SUCCEEDED iteration's
	// product value in that same order, so it is the value a terminator reads
	// while the loop is still running and the value the loop reports when it
	// closes.
	Iterations []LoopIteration

	// Selected is a branch node's routing decision (yaah-0u0v.15): the node path
	// of the arm it chose, or empty for "no case matched and no default was
	// declared". The arms a branch did not select read it — through the routing
	// relation the reducer keeps — to decide whether they may run at all.
	Selected string

	// Combined is an aggregate node's value (yaah-0u0v.15): the concatenated
	// list, the merged mapping, or a summarize body's product. It is kept
	// structured so `steps.<id>.output` and a selector read the value rather than
	// a re-parse of its text, while Result carries the text.
	Combined any

	// Candidates, Chosen, Selection, SelectPolicy, and Verdict are a select
	// node's own ledger (yaah-0u0v.20): what it chose among, which one won, the
	// winning value, the effective policy with the folded default applied, and
	// the judge's delivered verdict body under `policy: judge`. They are kept
	// structured for the same reason Combined is — `steps.<id>.output.winner` is
	// what a downstream settle reads — while Result carries the winner's text.
	//
	// Chosen is the winner's index in Candidates and is meaningful only together
	// with a non-nil Selection: a node that has not decided yet records neither.
	Candidates   []Candidate
	Chosen       int
	Selection    any
	SelectPolicy ir.SelectPolicy
	Verdict      string

	// Disposition, GateAssert, GateReason, and GateVerdict are a gate node's own
	// ledger (yaah-0u0v.21): the answer it reached (pass, fail, or escalate), the
	// registry assert that reached it (empty under a judge), the one sentence the
	// check or the judge stated, and the judge's delivered verdict body (empty
	// under an assert). They are the node's VALUE — gateValue reads them into the
	// mapping `steps.<id>.output` resolves — which is what makes a gate's result a
	// value rather than control flow hidden in a prompt.
	Disposition string
	GateAssert  string
	GateReason  string
	GateVerdict string
}

// NodeOutput is one declared output binding the reducer recorded for a node.
// It is the value a later step reads, and the record a run prints: the name is
// the node's `output:` name (for an emit that declares none, the step id, which
// is the name `steps.<id>.output` already reads), the value is the delivered
// report text or the rendered emit value, and Artifact is the artifact path the
// node declared for it — recorded, not written, because writing is the host's
// door and not the scheduler's.
//
// Per-binding extraction of a structured result (reading one field out of a
// declared Report or Verdict) is value plumbing, and it landed with
// yaah-0u0v.13: references read through Run.stepValue, which resolves a
// delegate's recorded value as the envelope facts plus any JSON object body,
// and a settle node's as its settled record. The binding stored here is what
// the node itself produced — a report's delivered text, an emit's rendered
// value, a settle's settled record.
type NodeOutput struct {
	// NodePath and Kind name the producing node, so a flattened run output
	// needs no second lookup.
	NodePath string  `json:"node_path"`
	Kind     ir.Kind `json:"kind"`
	Name     string  `json:"name,omitempty"`
	Value    string  `json:"value,omitempty"`
	Artifact string  `json:"artifact_path,omitempty"`
	Contract string  `json:"contract,omitempty"`
}

// LastAttempt returns the node's most recent attempt, or nil.
func (n *NodeState) LastAttempt() *Attempt {
	if n == nil || len(n.Attempts) == 0 {
		return nil
	}
	return n.Attempts[len(n.Attempts)-1]
}
