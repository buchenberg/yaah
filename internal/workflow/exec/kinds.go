package exec

import (
	"strconv"
	"strings"

	wfassert "github.com/buchenberg/yaah/internal/workflow/assert"
	"github.com/buchenberg/yaah/internal/workflow/ir"
)

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
	// kindFanoutStep expands the node's items and schedules one body instance
	// per item (yaah-0u0v.14). Like a settle it dispatches nothing itself: the
	// node stays ready until the driver reports the resolved item list, and the
	// dispatches the expansion creates are the items', counted against the
	// global cap and the fan-out's own max_parallel. That is why a fan-out is
	// never handed to the door as an attempt of its own.
	kindFanoutStep
	// kindLoopStep runs the node's body as a sequence of iterations
	// (yaah-0u0v.15). It dispatches nothing itself: the node stays ready, the
	// driver asks for one iteration at a time once the previous one has
	// terminated, and the iteration's own nodes are the dispatches — counted
	// against the global cap and the budget axes like any other node's.
	kindLoopStep
	// kindBranchStep routes (yaah-0u0v.15). It dispatches nothing and expands
	// nothing: the driver resolves the routed value, and the reducer records the
	// chosen arm and skips the ones the branch did not select.
	kindBranchStep
	// kindAggregateStep combines the node's inputs (yaah-0u0v.15). A
	// deterministic strategy (concat, json_merge) is a value step in the reducer;
	// summarize instantiates the node's body, and those instances are the
	// dispatches. Neither is an attempt of the aggregate's own.
	kindAggregateStep
	// kindGateStep evaluates the node's named assert (yaah-0u0v.21). It
	// dispatches nothing: the check is a Go function in a fixed registry, so the
	// whole evaluation happens inside a reducer cycle and costs no budget. Under
	// `judge` the gate's JUDGE is what dispatches, so that shape classifies as
	// kindDispatch instead and this step never sees it.
	kindGateStep
	// kindSelectStep chooses among the node's resolved candidates (yaah-0u0v.20).
	// Under a deterministic policy it dispatches nothing: the choice is a value
	// step in the reducer. Under policy: judge the node's JUDGE is what dispatches,
	// so that policy classifies as kindDispatch instead and this step only
	// resolves the candidate set the verdict will name one of.
	kindSelectStep
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
// implementation; a fan-out expands into item instances the ordinary scheduler
// then runs (yaah-0u0v.14); a loop, a branch, and an aggregate resolve
// themselves in the reducer and, where they run a body at all, expand into body
// instances the ordinary scheduler runs (yaah-0u0v.15). Everything else is
// refused with a reason naming the bead that owns it, for the reason given in the
// package doc: a container that reported success would contradict validate's
// proof of the same graph.
//
// A refusal that is a fact about the DECLARATION belongs here rather than in the
// driver: a loop with no bound, a bodyless loop, an aggregate with no inputs or an
// unknown strategy, and a fan-out join this core cannot honor all mean the node
// may not run at all, and saying so before the first instance dispatches is the
// honest order. Refusals that depend on run state — an unreadable `over` source, a
// terminator whose producer never succeeded — are the driver's, because no
// disposition table can know them.
func classifyKind(n *ir.Node) (kindAction, string) {
	switch n.Kind {
	case ir.KindDelegate:
		return kindDispatch, ""
	case ir.KindFanout:
		// join is decided here rather than at expansion: a join this core cannot
		// honor means the node may not run at all, and saying so before the
		// first item dispatches is the honest order. All three joins execute as
		// of yaah-0u0v.19 — all closes on its last item, any on the first
		// success, quorum on the Nth — and what can be refused at declaration
		// time is a quorum that is not a usable count. The other half of the
		// quorum bound (it cannot exceed the item count) needs the resolved item
		// list, so the driver states that one at expansion.
		switch joinKind(n) {
		case ir.JoinAll, ir.JoinAny:
			return kindFanoutStep, ""
		case ir.JoinQuorum:
			if n.Quorum == nil {
				return kindRefuse, ReasonFanoutQuorumInvalid + ": " + n.Path +
					" declares join: quorum with no quorum count; a quorum is at least 1 and at most the item count"
			}
			if *n.Quorum < 1 {
				return kindRefuse, ReasonFanoutQuorumInvalid + ": " + n.Path +
					" declares quorum: " + strconv.Itoa(*n.Quorum) + "; a quorum is at least 1"
			}
			return kindFanoutStep, ""
		}
		return kindRefuse, ReasonUnknownKind + ": fan-out join " + string(n.Join)
	case ir.KindLoop:
		// A loop with no bound has no number to iterate to: WF034 already reports
		// the declaration, and a deterministic core that invented a ceiling would be
		// inventing a host policy nobody authored.
		if n.MaxIterations == nil || *n.MaxIterations < 1 {
			return kindRefuse, ReasonLoopUnbounded + ": " + n.Path +
				" declares no max_iterations; the scheduler invents no bound"
		}
		if len(n.Body) == 0 {
			return kindRefuse, ReasonLoopBodyMissing + ": " + n.Path + " declares no body to iterate"
		}
		return kindLoopStep, ""
	case ir.KindBranch:
		return kindBranchStep, ""
	case ir.KindGate:
		// A gate is one computed condition plus two dispositions (yaah-0u0v.21).
		// Which half it is decides everything: an assert is a reducer step (a
		// registry check dispatches nothing), a judge IS a dispatch, and a gate
		// that names neither — or both — is refused here, before the call it
		// cannot decide is made. An assert name outside the registry is the same
		// kind of declaration fact: the registry is an allowlist, not a namespace
		// of free-form names, so a document naming a check this build does not
		// ship may not run at all.
		return classifyGate(n)
	case ir.KindAggregate:
		switch n.Strategy {
		case ir.AggregateConcat, ir.AggregateJSONMerge, ir.AggregateSummarize:
		default:
			return kindRefuse, ReasonAggregateStrategyUnsupported + ": " + n.Path + " strategy " +
				aggregateStrategyLabel(n.Strategy)
		}
		if len(n.Inputs) == 0 {
			return kindRefuse, ReasonAggregateInputsEmpty + ": " + n.Path + " declares no inputs to combine"
		}
		if n.Strategy == ir.AggregateSummarize && len(n.Body) == 0 {
			return kindRefuse, ReasonAggregateBodyMissing + ": " + n.Path +
				" declares strategy: summarize with no body to dispatch"
		}
		return kindAggregateStep, ""
	case ir.KindSettle:
		return kindSettleStep, ""
	case ir.KindSelect:
		// A select chooses among a candidate set a producer already collected
		// (yaah-0u0v.20). Under policy: judge the node's dispatch IS the judge
		// delegate, so it goes through the door as an attempt of this node; the
		// other two policies dispatch nothing and decide inside the reducer. A
		// judge policy with no judge payload is a declaration fact — nobody is
		// named to decide — so it is refused here, before the candidate set is
		// ever resolved, and its unknown-policy sibling is refused the same way.
		policy := selectPolicyOf(n)
		switch policy {
		case ir.SelectJudge:
			if n.Judge == nil {
				return kindRefuse, ReasonSelectJudgeMissing + ": " + n.Path +
					" declares policy: judge with no judge payload, so nobody is named to decide"
			}
			return kindDispatch, ""
		case ir.SelectFirstSuccess, ir.SelectSmallestDiff:
			return kindSelectStep, ""
		}
		return kindRefuse, ReasonSelectPolicyUnsupported + ": " + n.Path + " policy " + selectPolicyLabel(policy)
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

// dispatchRole is the role a node's dispatch carries. A select under policy
// judge (yaah-0u0v.20) and a gate whose condition is a judge (yaah-0u0v.21) both
// dispatch their JUDGE payload, so the judge's role is the attempt's identity;
// every other kind is its own role, and a node that names none leaves the choice
// to the lane.
func dispatchRole(n *ir.Node) string {
	if n == nil {
		return ""
	}
	if n.Judge != nil && (selectDispatchesJudge(n) || n.Kind == ir.KindGate) {
		return n.Judge.Role
	}
	return n.Role
}

// classifyGate is the gate arm of the kind table (yaah-0u0v.21).
func classifyGate(n *ir.Node) (kindAction, string) {
	switch {
	case n.Assert == nil && n.Judge == nil:
		return kindRefuse, ReasonGateDeclarationInvalid + ": " + n.Path +
			" declares neither assert nor judge, so nothing decides whether it passes"
	case n.Assert != nil && n.Judge != nil:
		return kindRefuse, ReasonGateDeclarationInvalid + ": " + n.Path +
			" declares both assert and judge; a gate has one condition, and two would leave the run choosing which answer wins"
	case n.Assert != nil:
		if _, ok := wfassert.Lookup(n.Assert.Name); !ok {
			return kindRefuse, ReasonGateAssertUnknown + ": " + n.Path + " " +
				wfassert.Problem(n.Assert.Name, n.Assert.Args)
		}
		if why := wfassert.Problem(n.Assert.Name, n.Assert.Args); why != "" {
			return kindRefuse, ReasonGateAssertArgs + ": " + n.Path + " " + why
		}
		return kindGateStep, ""
	}
	if strings.TrimSpace(n.Judge.Role) == "" || n.Judge.Prompt == nil || strings.TrimSpace(n.Judge.Prompt.Raw) == "" {
		return kindRefuse, ReasonGateJudgeMissing + ": " + n.Path +
			" declares a judge with no role or no prompt, so nobody is named to decide"
	}
	return kindDispatch, ""
}
