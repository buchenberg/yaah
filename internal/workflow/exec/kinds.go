package exec

import "github.com/buchenberg/yaah/internal/workflow/ir"

// Terminal reasons. The scheduler states a cause for every node it ends in a
// non-success state; an unexplained terminal node is a bug in this package.
const (
	// ReasonNotImplemented prefixes a node whose kind the core does not
	// execute yet. The suffix names the bead that owns the kind.
	ReasonNotImplemented = "not_implemented"
	// ReasonUpstreamSkipped prefixes a node skipped because a need of it was
	// skipped: its input was never produced.
	ReasonUpstreamSkipped = "upstream_skipped"
	// ReasonUpstreamCancelled prefixes a node cancelled because a need of it
	// was cancelled.
	ReasonUpstreamCancelled = "upstream_cancelled"
	// ReasonGuardFalse is a node its own `when` guard refused.
	ReasonGuardFalse = "when guard is false"
	// ReasonGuardError prefixes a guard that failed to evaluate.
	ReasonGuardError = "guard_error"
	// ReasonRuntimeError prefixes a door malfunction: no envelope exists, so
	// nothing is asserted about retryability and the node is terminal.
	ReasonRuntimeError = "runtime_error"
	// ReasonDispatchFailed prefixes a node whose attempts are done and whose
	// last envelope reported status=error.
	ReasonDispatchFailed = "dispatch_failed"
	// ReasonRunStopped prefixes a node the run stopped before it could start.
	ReasonRunStopped = "run_stopped"
	// ReasonCancelledByRun prefixes a running attempt the run cancelled.
	ReasonCancelledByRun = "cancelled_by_run"
	// ReasonUnknownKind prefixes a node whose kind is not in the resolved
	// vocabulary at all: a hand-built graph, not an authored document.
	ReasonUnknownKind = "unknown_kind"
)

// kindAction is what the fixed kind switch decides for a ready node.
type kindAction int

const (
	// kindDispatch hands the node to the Runtime door as an attempt.
	kindDispatch kindAction = iota
	// kindValueStep settles the node inside the reducer: it renders the
	// node's declared value against the run's scope and records the result.
	// It dispatches nothing, so it consumes no dispatch budget, and it
	// touches no filesystem — writing a declared artifact_path is the
	// host's step (internal/workflow/host), because a scheduler core that
	// wrote files would have a second door beside Runtime.
	kindValueStep
	// kindSettleStep settles the node's proposals through the door's settlement
	// path (yaah-0u0v.13). It is neither a dispatch nor a value step: it starts
	// no attempt and consumes no dispatch budget, because a settlement is a
	// world effect the lane performs, not a model call the run pays for — and
	// validate's cost model (validate/cost.go) prices only delegates, judges,
	// calls, and containers, so counting one here would put the run over a
	// ceiling the graph's own estimate never charged it for.
	kindSettleStep
	// kindRefuse ends the node failed with ReasonNotImplemented.
	kindRefuse
)

// classifyKind is the whole node-kind disposition table. There is no registry,
// no plugin path, and no dynamic dispatch — the spec's WD1 decision that the
// node vocabulary is closed is enforced here by a switch over ir.Kind, which
// the compiler checks against the resolved enum's meaning, and by the default
// arm, which refuses anything outside it.
//
// A delegate dispatches through the door; an emit settles as a value step in
// the reducer (yaah-0u0v.12); a settle disposes of proposals through the
// door's settlement path (yaah-0u0v.13), which is an adapter over
// internal/dispatch's existing Settle rather than a second settlement
// implementation. Everything else is refused with a reason naming the bead
// that owns it, for the reason given in the package doc: a container that
// reported success would contradict validate's proof of the same graph.
func classifyKind(n *ir.Node) (kindAction, string) {
	switch n.Kind {
	case ir.KindDelegate:
		return kindDispatch, ""
	case ir.KindFanout:
		return kindRefuse, ReasonNotImplemented + ": fan-out items and join are yaah-0u0v.14"
	case ir.KindLoop:
		return kindRefuse, ReasonNotImplemented + ": loop iteration is yaah-0u0v.15"
	case ir.KindBranch:
		return kindRefuse, ReasonNotImplemented + ": branch arm selection is yaah-0u0v.15"
	case ir.KindGate:
		return kindRefuse, ReasonNotImplemented + ": gate assertion and judge are yaah-0u0v.15"
	case ir.KindAggregate:
		return kindRefuse, ReasonNotImplemented + ": aggregate combine is yaah-0u0v.15"
	case ir.KindSettle:
		return kindSettleStep, ""
	case ir.KindSelect:
		// Select chooses among candidates collected by a fanout/aggregate
		// producer; the collection arrives with yaah-0u0v.14, so candidate
		// selection has nothing to choose among until then.
		return kindRefuse, ReasonNotImplemented + ": candidate selection is yaah-0u0v.14"
	case ir.KindCall:
		return kindRefuse, ReasonNotImplemented + ": nested workflow calls are not in the scheduler core"
	case ir.KindEmit:
		return kindValueStep, ""
	case ir.KindHuman:
		return kindRefuse, ReasonNotImplemented + ": a human response is an approval door, not the scheduler core"
	}
	return kindRefuse, ReasonUnknownKind + ": node kind " + string(n.Kind)
}

// dispatched reports whether a ready node is something the driver may hand to
// the door. The driver starts nodes in the ready queue's canonical order and
// skips what the reducer has already settled, so a refused or guarded node
// never reaches the door and never consumes a dispatch budget.
func dispatched(n *ir.Node) bool {
	action, _ := classifyKind(n)
	return action == kindDispatch
}
