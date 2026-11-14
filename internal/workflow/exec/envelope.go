package exec

import "time"

// Completion is the scheduler's view of the ONE dispatch completion envelope
// (yaah-was9.1). It is the same JSON contract the model-facing surface reads —
// every field name below is the envelope's own tag — but it is a separate
// type, because the scheduler core may not import internal/dispatch: that
// package links internal/tools/substrate, and this package must have no
// internal/tools dependency at all. The producer that bridges the two is the
// dispatch adapter (wired outside this package), and envelope_parity_test.go
// pins the mirror by decoding an envelope rendered by dispatch.Run's own
// result type into Completion and checking the scheduler-visible facts
// survive.
//
// The scheduler reads exactly three things from a completion: whether the
// status says the attempt completed, whether the envelope's recovery block
// asserts the work is worth re-dispatching, and the facts to record. It never
// inspects error prose to decide either of the first two.
type Completion struct {
	// Status is the envelope status: proposal, report, shared, completed, or
	// error.
	Status string `json:"status"`
	// DispatchID names the dispatch that produced this completion.
	DispatchID string `json:"dispatch_id,omitempty"`
	Role       string `json:"role,omitempty"`
	Lane       string `json:"lane,omitempty"`
	World      string `json:"world,omitempty"`
	Model      string `json:"model,omitempty"`
	// DurationMs is the wall-clock span the lane reported for the dispatch.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// StopReason is the envelope's typed reason: completed, turn_budget,
	// iteration_budget, max_tokens, timeout, stuck, halted, canceled, or
	// provider_error.
	StopReason string `json:"stop_reason,omitempty"`
	// ProposalID is set when the attempt sealed a proposal. The scheduler
	// records it and does nothing else with it: settlement verbs are
	// yaah-0u0v.13's, and a node's lifecycle deliberately leaves the
	// proposal it produced open rather than disposing of it here.
	ProposalID string `json:"proposal_id,omitempty"`
	// Result is the delivered text or the sealed envelope's result body.
	Result string `json:"result,omitempty"`
	// Error is the untruncated cause of a status=error completion.
	Error   string `json:"error,omitempty"`
	Partial bool   `json:"partial,omitempty"`
	// Recovery is the envelope's recovery block. A nil Recovery means the
	// producer asserted nothing about re-dispatch, which is not an assertion
	// that a retry is allowed.
	Recovery *Recovery `json:"recovery,omitempty"`
	// Contract is the envelope's `contract` block (yaah-was9.2): the per-role
	// response contract the delivered result was checked against. It is
	// mirrored verbatim and never synthesized — a nil Contract means the lane
	// asserted nothing, which is what every dispatch lane does today while the
	// field is reserved. Absent is not "checked and passed", and a typed
	// workflow output that needs a verdict reads this field instead of
	// running its own validator.
	Contract *Contract `json:"contract,omitempty"`
}

// Contract mirrors dispatch.ResultContract: the name of the contract a result
// was checked against and the verdict, with the same JSON field names so the
// envelope parity test can decode one into the other.
type Contract struct {
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

// ContractStatus is the envelope's contract verdict, verbatim, or empty when
// the envelope carried no contract block.
func (c Completion) ContractStatus() string {
	if c.Contract == nil {
		return ""
	}
	return c.Contract.Status
}

// Recovery mirrors the envelope's recovery block: the one place the runtime
// says whether running the work again can make progress.
type Recovery struct {
	Retryable   bool   `json:"retryable"`
	Recommended string `json:"recommended,omitempty"`
}

// Envelope status values, verbatim from the dispatch vocabulary.
const (
	StatusProposal  = "proposal"
	StatusReport    = "report"
	StatusShared    = "shared"
	StatusCompleted = "completed"
	StatusError     = "error"
)

// Completed reports whether the envelope says the attempt finished its work.
// An empty or unknown status is not a completion: the scheduler refuses to
// credit success to a lane that could not name its outcome, because a
// succeeded node with no result is exactly the silent divergence from the
// envelope this package exists to avoid.
func (c Completion) Completed() bool {
	switch c.Status {
	case StatusProposal, StatusReport, StatusShared, StatusCompleted:
		return true
	}
	return false
}

// Retryable is the envelope's assertion, never a derivation. A missing
// recovery block reads as false.
func (c Completion) Retryable() bool {
	return c.Recovery != nil && c.Recovery.Retryable
}

// Duration is the reported wall-clock span.
func (c Completion) Duration() time.Duration {
	return time.Duration(c.DurationMs) * time.Millisecond
}
