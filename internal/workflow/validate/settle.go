package validate

import (
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// The rules in this file are the local half of the linearity family: each one
// reads the graph (or one boundary state the analysis computed) at a single
// declaration, rather than asking what a run can do across paths. They are
// still linearity rules — every one of them is about a proposal's disposition,
// a set it is drawn from, or the concurrency that decides whether two
// settlements can land at once.

// -------------------------------------------------------------------- WF020

// settleTargetsProps checks WF020: `settle.proposals` refers to a
// proposal-lane collection.
//
// WF011 already owns the declared *type* half of this — a producer typed
// `Verdict` cannot be a Proposal, and a settle that reads a scalar is not
// reading a set — so what is left here is the lane and the kind, which the
// type system cannot see. A report-lane dispatch retains nothing: the
// sub-agent returns a report and the harness has no worktree to land, so a
// settle pointed at its output or at the collect of a body made of such
// dispatches is a step that can never settle anything. The same is true of a
// step whose kind has no dispatch at all: an emit writes an artifact, a gate
// yields a verdict, a settle yields a Settled record, and none of them is a
// proposal.
//
// An unset lane is not evidence. resolve folds defaults, and the default lane
// is the one that retains, so the rule fires on a declared contradiction only
// — the shallow-but-explicit discipline the whole package keeps.
func settleTargetsProps(l *linearity) []Diagnostic {
	var out []Diagnostic
	for _, n := range l.order {
		if n.Kind != ir.KindSettle || n.Proposals == nil || n.Proposals.From == nil {
			continue
		}
		set := l.handleFor(n, n.Proposals)
		if !set.attributed {
			continue
		}
		if len(set.referents) == 0 {
			continue // an empty body or an unresolvable step: WF002/WF010/WF021 own it
		}
		if len(set.producers()) > 0 {
			continue
		}
		at := n.Path + ".proposals"
		if reports := set.reports(); len(reports) > 0 {
			out = append(out, diag(CodeWF020, at,
				"refers to %s, a report-lane dispatch that retains no proposal; a settle lands worktrees, so point it at a proposal-lane producer",
				reports[0].Path))
			continue
		}
		// Every referent is a step of a kind that cannot carry a proposal.
		if !allYieldNothing(set.referents) {
			continue
		}
		out = append(out, diag(CodeWF020, at,
			"refers to %s, which yields no Proposal value; a settle lands worktrees, so point it at a proposal-lane producer",
			set.String()))
	}
	return out
}

// allYieldNothing reports whether every step named by a handle belongs to a
// kind that produces no proposal. A call is left out on purpose: this graph
// cannot see the workflow it invokes, so the value it returns could be a
// proposal and this package cannot say otherwise. A fan-out or loop's own
// output is left out for the same reason in the other direction — reading a
// container's `output` rather than its `collect` is odd, but the container's
// body is what decides it and that is WF011's shape question.
func allYieldNothing(nodes []*ir.Node) bool {
	for _, n := range nodes {
		switch n.Kind {
		case ir.KindEmit, ir.KindGate, ir.KindSettle, ir.KindBranch, ir.KindHuman, ir.KindAggregate:
			continue
		default:
			return false
		}
	}
	return len(nodes) > 0
}

// -------------------------------------------------------------------- WF022

// settleAfterRelease checks WF022: a `select` or `apply` target is not a
// proposal some earlier step released or discarded.
//
// release returns a worktree and discard closes a proposal without landing it;
// both are terminal, and a settlement step downstream that names the same
// proposal is asking the harness to land something it has already thrown away.
// The mirror case — discard after apply — is WF021's double settlement, and the
// two rules read the same boundary state from opposite ends: this one looks at
// the verbs already applied where the second one looks at the count.
//
// The finding says "may already have": the state is a may-join, so on a graph
// where only one branch releases the proposal, an apply that both arms can
// reach is reported for the arm that did. That is the conservative direction
// for this rule — a run that lands a discarded proposal is worse than a
// warning an author narrows away by adding a `needs`.
func settleAfterRelease(l *linearity) []Diagnostic {
	var out []Diagnostic
	for _, n := range l.order {
		if n.Kind != ir.KindSettle {
			continue
		}
		if n.Action != ir.SettleApply && n.Action != ir.SettleSelect {
			continue
		}
		for _, id := range l.handleFor(n, n.Proposals).ids() {
			in := l.entry[n][id]
			if in.acts&(dispRelease|dispDiscard) == 0 {
				continue
			}
			out = append(out, diag(CodeWF022, n.Path+".proposals",
				"%ss a proposal from %s that %s upstream may already have %s, and a released or discarded worktree cannot be landed",
				n.Action, id, strings.Join(in.by, ", "), in.acts))
		}
	}
	return out
}

// -------------------------------------------------------------------- WF023

// parallelApplies checks WF023: parallel `apply` branches declare disjoint
// path intent, or are serialized by needs.
//
// Two settlement steps are parallel when no path runs either of them after the
// other — the relation the analysis built from needs, from the data
// dependency a reference states, and from branch and gate arms. Parallel is
// what makes the check necessary at all: two applies that both run cannot wait
// for each other's merge, so the plan has to say how they avoid each other.
//
// Two findings come out of that, in the order the author can act on them:
//
//   - both applies name the same proposal. That is not a disjointness question,
//     whatever the policy says: one worktree would be landed twice by two steps
//     that never saw each other. `path_disjoint` does not license it, since the
//     policy decides what a settle may co-land, not what it may re-land.
//   - both are applies and one of them runs `three_way`. An empty policy means
//     the runtime default `path_disjoint`, which is a declaration of disjoint
//     path intent and is accepted as one; `three_way` is the opposite
//     declaration — it says the merge will reconcile whatever overlaps — and a
//     three-way merge of two landing runs needs the other run finished first.
//     The fix is the edge the author left out, so the finding asks for it by
//     name.
//
// Overlap is decided on token identity, which is the graph's finest available
// notion of "the same proposal": two settles whose handles are set-disjoint are
// two settles on different work, and two whose handles name the same producer
// are not.
func parallelApplies(l *linearity) []Diagnostic {
	var applies []*ir.Node
	for _, n := range l.order {
		if n.Kind != ir.KindSettle {
			continue
		}
		if n.Action != ir.SettleApply && n.Action != ir.SettleSelect {
			continue
		}
		applies = append(applies, n)
	}
	var out []Diagnostic
	for i, b := range applies {
		bset := l.handleFor(b, b.Proposals)
		if len(bset.ids()) == 0 {
			continue
		}
		for _, a := range applies[:i] {
			aset := l.handleFor(a, a.Proposals)
			if len(aset.ids()) == 0 {
				continue
			}
			if l.reachable(a, b) || l.reachable(b, a) {
				continue // ordered by needs, a data dependency, or an arm: not parallel
			}
			at := b.Path
			if shared := intersectIDs(aset.ids(), bset.ids()); len(shared) > 0 {
				out = append(out, diag(CodeWF023, at+".proposals",
					"and %s settle %s in parallel, with neither ordered by needs against the other: one proposal cannot be landed twice",
					a.Path, strings.Join(shared, ", ")))
				continue
			}
			if a.SettlePolicy != ir.SettleThreeWay && b.SettlePolicy != ir.SettleThreeWay {
				continue // both declared disjoint path intent
			}
			which := b.Path
			if b.SettlePolicy != ir.SettleThreeWay {
				which = a.Path
			}
			out = append(out, diag(CodeWF023, at+".policy",
				"settles in parallel with %s while %s runs policy: three_way, which declares no disjoint path intent: order the two with needs so only one merge is live at a time",
				a.Path, which))
		}
	}
	return out
}

func intersectIDs(a, b []string) []string {
	var out []string
	for _, x := range a {
		for _, y := range b {
			if x == y {
				out = append(out, x)
				break
			}
		}
	}
	return out
}

// -------------------------------------------------------------------- WF026

// emptyNeedsPolicy checks WF026: a settle over a possibly-empty proposal set
// declares `on_empty`.
//
// `where` is the loud case and the one the plan names: a filter is written
// precisely because the author does not know whether anything matches, and a
// settle that matches nothing has to say whether that is success, failure, or
// the run stopping. `pass` on a set that came back empty because the only
// reviewer failed is a workflow that silently landed nothing, which is the
// outcome the declaration exists to prevent.
//
// The other possibly-empty handles are the ones whose size the document never
// states: a fan-out over a run-time list, which can resolve to zero items; a
// loop, whose collect is sized by its iterations and its filters; a `params`
// or item source, which is a value rather than a producer; and a single output
// read without `as_list`, whose shape the value family has already refused or
// cannot see. Each is a set the author cannot prove inhabited, and `on_empty`
// is one word.
func emptyNeedsPolicy(l *linearity) []Diagnostic {
	var out []Diagnostic
	for _, n := range l.order {
		if n.Kind != ir.KindSettle || n.Proposals == nil || n.Proposals.From == nil {
			continue
		}
		if n.OnEmpty != "" {
			continue
		}
		if !l.handleFor(n, n.Proposals).maybeEmpty {
			continue
		}
		out = append(out, diag(CodeWF026, n.Path+".on_empty",
			"settles a set that can be empty (%s) and declares no on_empty: say whether matching nothing is pass, fail, or skip",
			n.Proposals.From.String()))
	}
	return out
}

// -------------------------------------------------------------------- WF028

// selectionDecidable checks WF028: `select` candidates are non-empty and the
// policy's required judge is declared.
//
// A selection policy is a function over a set, and one of its arguments is
// undeclared in each of the two ways this fires. `policy: judge` without a
// `judge:` payload names nobody to decide, so the run has no rule for picking
// among the candidates; the converse, a judge written under a policy that never
// consults one, is a dispatch the workflow will not pay for, which is the same
// kind of dead declaration WF014 reports when the resolver drops a field. An
// undeclared policy is neither: what the default policy does is the executor's
// answer, not a gap in this document.
//
// A candidate set the graph can prove is empty is the other half: three
// workstreams written out as literals is a set of three, and an empty literal
// list is a selection over nothing, which no policy can decide. A set whose
// size is only known at run time is not a finding here — that is WF026's
// question, asked of a settle.
func selectionDecidable(l *linearity) []Diagnostic {
	var out []Diagnostic
	for _, n := range l.order {
		if n.Kind != ir.KindSelect {
			continue
		}
		if n.Policy == ir.SelectJudge && n.Judge == nil {
			out = append(out, diag(CodeWF028, n.Path+".judge",
				"policy: judge selects among candidates by dispatching a verdict, and no judge is declared: name the role and prompt that decide"))
		} else if n.Policy != "" && n.Policy != ir.SelectJudge && n.Judge != nil {
			out = append(out, diag(CodeWF028, n.Path+".judge",
				"declares a judge while policy: %s never consults one: either choose policy: judge or drop the judge dispatch",
				n.Policy))
		}
		if n.Candidates == nil || n.Candidates.From == nil {
			continue
		}
		if !l.handleFor(n, n.Candidates).provablyEmpty {
			continue
		}
		out = append(out, diag(CodeWF028, n.Path+".candidates",
			"selects from %s, which the document proves is empty: no policy can choose among nothing",
			n.Candidates.From.String()))
	}
	return out
}

// -------------------------------------------------------------------- WF029

// dynamicFanoutBudget checks the clause of WF029 that WF034 cannot reach.
//
// WF034 already refuses a fan-out with no `max_parallel`, for every fan-out,
// static or dynamic, so re-reporting the missing number here would be the same
// finding twice. What WF034 does not look at is the budget the workflow
// declares around it, and that is the half that only becomes interesting when
// the item list is a run-time value: with `over:` written as literals the
// document states the item count and WF033 can price the whole run, but with a
// selector the item count is unknown, so `max_parallel` is the only number in
// the document bounding how many dispatches are live at once — and
// `budgets.max_concurrent_dispatches` is the run's own ceiling on exactly that.
//
// A plan whose widest concurrency exceeds its declared ceiling is
// unrealizable rather than expensive: the scheduler will hold items back and the
// workflow will not run in the shape the author wrote. Nested fan-outs are
// multiplied, because an inner fan-out runs once per outer item and its
// concurrency is live per enclosing item.
func dynamicFanoutBudget(g *ir.Graph) []Diagnostic {
	if g.Budgets == nil || g.Budgets.MaxConcurrentDispatches == nil {
		return nil // nothing declared to cover the fan-out
	}
	limit := *g.Budgets.MaxConcurrentDispatches
	var out []Diagnostic
	var walk func(nodes []*ir.Node, live int)
	walk = func(nodes []*ir.Node, live int) {
		for _, n := range nodes {
			if n == nil {
				continue
			}
			if n.Kind == ir.KindFanout && n.MaxParallel != nil && n.Over != nil && n.Over.Selector != nil {
				want := mulCap(live, *n.MaxParallel)
				if want > limit {
					out = append(out, diag(CodeWF029, n.Path+".max_parallel",
						"fans out over a run-time list with max_parallel %d (%d live dispatches including its enclosing fan-out), over the %d declared by budgets.max_concurrent_dispatches: with no literal item count this is the only bound the document states",
						*n.MaxParallel, want, limit))
				}
			}
			child := live
			if n.MaxParallel != nil && *n.MaxParallel > 0 {
				child = mulCap(live, *n.MaxParallel)
			}
			if len(n.Body) > 0 {
				walk(n.Body, child)
			}
		}
	}
	walk(g.Steps, 1)
	return out
}
