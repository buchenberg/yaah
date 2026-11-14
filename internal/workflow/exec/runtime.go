package exec

import (
	"context"
	"time"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Handle names one started attempt at the door. The scheduler mints the
// DispatchID and the Runtime hands back whatever it needs to find the attempt
// again; a Runtime that keys on the id alone ignores the field.
type Handle struct {
	DispatchID string
	// Ref is the adapter's own reference — a background job id, a scope id.
	Ref string
}

// Dispatch is one attempt the run is asking the door to start. It carries the
// immutable IR node rather than a flattened argument struct: the node already
// holds the complete authored payload (role, prompt template, lane, provider,
// model, max_turns, grants, network, world, seed/continue/fork bindings), and
// a second shape would be a second source of truth that could drift from the
// graph the validator checked.
type Dispatch struct {
	Node    *ir.Node
	Attempt int
	// DispatchID is "<node path>#<attempt>", the same identity the envelope
	// reports back, so a trace can join the request and the completion.
	DispatchID string
	// Scope is the immutable snapshot of run state at start time. The
	// adapter renders the node's templates against it. It is a snapshot and
	// not the live run because a Runtime is allowed to read it after other
	// nodes have terminated; reading live scheduler state from an adapter
	// would be a data race against the event loop.
	Scope ir.Scope
	// Timeout is the node's declared timeout_seconds, converted. Zero means
	// the node declared none. The scheduler does not implement timeouts — it
	// has no timers — so this is plumbing for the lane that does.
	Timeout time.Duration
	// Deadline is the run's wall-clock budget expiry, when one is declared.
	// Zero means undeclared.
	Deadline time.Time
}

// SettleRequest is one settlement the run is asking the door to perform. It
// names a proposal by the id the sealing envelope reported and the verb the
// author declared; everything else a settlement needs — the registry, the
// world, the merge strategy — belongs to the lane, and the door is the only
// place that reaches it.
//
// Policy may be empty. An empty policy means the author declared none, and the
// runtime default (path_disjoint) is the door's interpretation, not
// validate's: the validator deliberately does not materialize it, so the
// resolved graph keeps saying "undeclared" and the adapter says what it did.
type SettleRequest struct {
	// NodePath is the settle node asking for the disposition.
	NodePath string
	// ProposalID is the sealed proposal to dispose of.
	ProposalID string
	// Producer is the node path that sealed it, when a handle records one.
	Producer string
	// Verb is one of select, apply, release, discard. The scheduler maps its
	// resolved action onto these four and refuses anything else: continue and
	// fork are delegate re-run bindings, not settlement verbs.
	Verb string
	// Policy is the declared settle policy, or empty when undeclared.
	Policy ir.SettlePolicy
}

// Runtime is the only door through which a workflow run touches the world.
// Everything here is a side effect or a clock: no method returns scheduler
// state, and no method may call Run.Apply.
//
// The shape is deliberately start/poll rather than a blocking call, so the
// scheduler stays single-threaded: one goroutine owns the Run, asks the door
// for starts, and commits completions in canonical order. A blocking Dispatch
// would either serialize the whole graph or move state mutation into the
// callbacks of whatever goroutine finished first, and the second option is
// precisely the parallel state this package refuses to have. A real adapter
// therefore runs its dispatch in its own goroutine (typically
// internal/dispatch.Run) and reports it through Completed.
type Runtime interface {
	// Start launches one attempt and returns as soon as it is running. It
	// must not block on the attempt's completion. A Start that fails has
	// dispatched nothing, and the run records the failure through the door
	// malfunction path rather than inventing an envelope.
	Start(ctx context.Context, d Dispatch) (Handle, error)

	// Settle disposes of one proposal through the lane's existing settlement
	// path and reports what the world did. It is the one door method the
	// scheduler calls synchronously, and it is deliberately not a start/poll
	// pair: a settlement is a world effect with one answer, the run asks for
	// one per cycle in the plan's fixed order, and two settles may never work
	// the same proposal at once (validate's WF023 orders any two settlement
	// steps that could). A door that cannot settle reports an error and states
	// nothing about the proposal; a conflict is an outcome, not an error.
	Settle(ctx context.Context, req SettleRequest) (Settled, error)

	// Completed reports the envelope for a started attempt when the attempt
	// has finished. It must not block, and its answer must be stable: an
	// attempt once reported complete stays complete, because the scheduler
	// polls the oldest outstanding attempt until its turn to commit comes
	// and must not lose a completion it looked at early.
	Completed(h Handle) (Completion, bool)

	// Block waits until at least one started attempt has become completable,
	// or ctx is done. It exists so a real adapter can idle without polling,
	// and a synchronous fake returns immediately. Returning nil when nothing
	// became completable is a door malfunction, not a spurious wake: the
	// scheduler treats it as an error rather than spinning or hanging on it.
	Block(ctx context.Context) error

	// Cancel asks a running attempt to stop. The run's state does not wait
	// for the world to agree: the node is cancelled the moment the request is
	// made, and a late envelope is dropped. An error here is recorded on the
	// node so an adapter that could not stop real work is visible.
	Cancel(h Handle) error

	// Backoff waits out a node's declared retry backoff. A test fake advances
	// its virtual clock and returns immediately: tests never sleep.
	Backoff(ctx context.Context, d time.Duration) error

	// Now reads the run clock. The scheduler only ever samples it at event
	// boundaries — it arms no timers — so a virtual clock in a fake makes
	// wall-clock budget behavior deterministic.
	Now() time.Time
}
