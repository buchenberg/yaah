package validate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Rule codes for the proposal linearity family. This is the last family the
// plan calls for, so the catalog in doc.go is complete from here on.
//
// Two of these codes sit slightly outside the original one-line summaries and
// each does so for a reason recorded at its rule, in the same voice as WF014:
//
//	WF025 is written against an `on_abort` field the v1 vocabulary does not
//	       have — it is scheduled with the executor — so what this package can
//	       prove is the exposure such a field would cover, not the
//	       declaration. The rule says so in its own message.
//	WF029 keeps only the clause WF034 cannot reach: WF034 already demands
//	       max_parallel from every fan-out, so what is left is whether the
//	       enclosing budget covers it.
const (
	// CodeWF020 marks a settle whose `proposals:` handle provably cannot
	// denote a proposal: a report-lane dispatch, or a step of a kind that
	// yields no proposal at all.
	CodeWF020 = "WF020"
	// CodeWF021 marks a proposal that is not disposed of exactly once on
	// every reachable path — a proposal no settlement step ever takes, and a
	// proposal a second step takes after it was already settled.
	CodeWF021 = "WF021"
	// CodeWF022 marks a select or apply whose target a settlement step
	// upstream may already have released or discarded.
	CodeWF022 = "WF022"
	// CodeWF023 marks two settlement steps that can run concurrently without
	// declaring disjoint path intent or being ordered by a dependency.
	CodeWF023 = "WF023"
	// CodeWF024 marks `on_conflict: skip` on a set of more than one proposal
	// with no later settle that could catch what it skipped.
	CodeWF024 = "WF024"
	// CodeWF025 marks a run that can finish with proposals still open. The
	// vocabulary has no `on_abort` to declare, so this is the residue the
	// planned field would discharge.
	CodeWF025 = "WF025"
	// CodeWF026 marks a settle over a possibly-empty proposal set with no
	// `on_empty`.
	CodeWF026 = "WF026"
	// CodeWF027 marks a continue_from or fork_from that does not name one
	// proposal from the same role and lane, or a fork that leaves its source
	// with no disposition anywhere downstream.
	CodeWF027 = "WF027"
	// CodeWF028 marks a select with a candidate set that is provably empty,
	// or a judge policy with no judge declared.
	CodeWF028 = "WF028"
	// CodeWF029 marks a fan-out over a run-time-sized list whose declared
	// concurrency the workflow's own budget does not cover.
	CodeWF029 = "WF029"
)

// Linearity runs the proposal linearity family (WF020–WF029) over a resolved
// graph and returns every finding, rules in code order and within a rule in
// document order, so the slice is deterministic without the caller sorting.
//
// The family is a dataflow analysis (see proposal.go) and the rules are views
// over one fixed point, which is what lets WF021 answer "on every reachable
// path" instead of "somewhere in the graph". Four approximations come with the
// collapse that makes a repeated body checkable, and each is a decision rather
// than an oversight:
//
//   - Instances are not counted. A token is a producing node, not a proposal
//     per item or per iteration, because a settlement step is written over the
//     set. A run that settles three items' proposals through one collect
//     expression proves three dispositions here, and that is the right answer
//     for a validator: the author's mistake is a missing or duplicated
//     *expression*, never a duplicated item.
//   - One iteration represents a loop body. The body's own dispositions do not
//     feed its next iteration, so a proposal produced and settled inside a
//     body twice across iterations reads as once.
//   - A filtered or conflict-skipped settle counts as naming the set it
//     targets, not as disposing of every element of it. Partial coverage is
//     WF024's and WF026's subject, and charging it to WF021 as well would flag
//     every `where:` in the corpus instead of the sets that really have nobody
//     downstream.
//   - Only proposals the document itself claims are counted as leakable. A
//     token enters the plan when a settle, a selection, or a continuation
//     reaches for it, or when its dispatch declares the proposal lane or a
//     Proposal output type; a graph that never mentions settlement has no
//     settlement plan to prove, which is the same shallow-but-explicit
//     discipline the value family keeps. See claimScope.
//   - A select decides its candidates. The rejected proposals are closed by
//     the selection and the winner is a fresh token still owed a disposition;
//     the alternative reading makes every speculative workflow leak each
//     candidate it did not pick.
func Linearity(g *ir.Graph) []Diagnostic {
	if g == nil {
		return nil
	}
	l := analyze(g)
	var out []Diagnostic
	out = append(out, settleTargetsProps(l)...)
	out = append(out, settledOnce(l)...)
	out = append(out, settleAfterRelease(l)...)
	out = append(out, parallelApplies(l)...)
	out = append(out, skippedNeedsCatch(l)...)
	out = append(out, openAtExit(l)...)
	out = append(out, emptyNeedsPolicy(l)...)
	out = append(out, continuationTargets(l)...)
	out = append(out, selectionDecidable(l)...)
	out = append(out, dynamicFanoutBudget(g)...)
	return out
}

// -------------------------------------------------------------------- WF021

// settledOnce checks WF021: every proposal is disposed of exactly once on
// every reachable path.
//
// Three findings are available from the boundary states, and they are the
// three ways a run leaks or double-lands:
//
//   - a token still undisposed where a run can finish — the plain leak,
//     reported at its producer, because that is the dispatch the author has to
//     add a settle for;
//   - a token a settlement step names when a path reaching that step had
//     already disposed of it — the double settle, reported at the second
//     disposer's `proposals`, because that is the expression to narrow;
//   - a token produced inside a body whose container declares no collect,
//     which no handle outside can name: the run cannot end any other way, and
//     the fix is the container's missing `collect:` rather than a settle.
//
// The leak and WF025's finding overlap on purpose, in the same shape as
// WF003's cycle against WF004's stranded steps: WF021 names the proposal, and
// WF025 says the run as a whole can finish with work undecided. A double
// settle also counts its supersession, so a proposal that is continued and
// then also landed by a settle is reported — the continuation closed the
// parent, and landing it afterwards is the bug.
//
// The leak half speaks only about tokens in the document's settlement plan
// (see claimScope): a dispatch whose lane nobody wrote down and that no handle
// ever reaches for is not a proposal this author undertook to settle. The
// double-settle half is unaffected — the settle that names the proposal is
// itself the claim.
func settledOnce(l *linearity) []Diagnostic {
	var out []Diagnostic
	for _, id := range l.tokensEverywhere() {
		producer := l.byPath(id)
		if producer == nil || !l.claimed[id] {
			continue
		}
		if l.stranded[producer] {
			if container := l.parent[producer]; container != nil {
				out = append(out, diag(CodeWF021, producer.Path,
					"proposal is produced inside %s, which declares no collect: no step outside the body can name it, so nothing can settle it",
					container.Path))
			}
			continue
		}
		s, ok := l.runExit[id]
		if !ok {
			// The token never reaches a run end: it was dropped at a
			// container boundary that collected it, or nothing downstream can
			// see it. Either way the exit rules have no path to speak about.
			continue
		}
		if s.counts&countNone != 0 {
			out = append(out, diag(CodeWF021, producer.Path,
				"proposal is never settled on every reachable path: no settlement step disposes of it, so the run ends with it open"))
		}
	}
	for _, n := range l.order {
		if n.Kind != ir.KindSettle {
			continue
		}
		for _, id := range l.handleFor(n, n.Proposals).ids() {
			in := l.entry[n][id]
			if in.counts&(countOnce|countTwice) == 0 {
				continue
			}
			out = append(out, diag(CodeWF021, n.Path+".proposals",
				"settles a proposal produced at %s that is already disposed of on the paths reaching it (%s by %s); a proposal must have exactly one disposition",
				id, in.acts, strings.Join(in.by, ", ")))
		}
	}
	return out
}

// -------------------------------------------------------------------- WF024

// skippedNeedsCatch checks WF024: `on_conflict: skip` has a later settle that
// catches the skipped set.
//
// skip is the verb an author reaches for when a conflict is expected and
// tolerable — but tolerating it moves the conflicting proposals out of this
// settle and leaves them open, so something downstream has to take them or the
// run ends undecided. `escalate` and `stop` need no catch because neither
// pretends the run can carry on as if the proposal had landed.
//
// Two exemptions keep the rule honest about what the graph can show. A set of
// one has no two elements to conflict, so `on_conflict` on it is inert and no
// catch is owed — that is speculative's land_one, settling the single winner a
// select produced. And the catch is looked for anywhere downstream rather than
// on every downstream path: a settle that could catch the skipped proposals is
// enough to say the author thought about them, and demanding it on every path
// would be a claim about branch structure this family cannot make without
// duplicating WF021's whole analysis.
func skippedNeedsCatch(l *linearity) []Diagnostic {
	var out []Diagnostic
	for _, n := range l.order {
		if n.Kind != ir.KindSettle || n.OnConflict != ir.OnConflictSkip {
			continue
		}
		set := l.handleFor(n, n.Proposals)
		if set.singleton {
			continue
		}
		ids := map[string]bool{}
		for _, id := range set.ids() {
			ids[id] = true
		}
		if len(ids) == 0 {
			continue
		}
		var catches []*ir.Node
		for _, other := range l.order {
			if other == n || !l.reachable(n, other) {
				continue
			}
			if other.Kind != ir.KindSettle && other.Kind != ir.KindSelect {
				continue
			}
			var s *ir.Selector
			switch other.Kind {
			case ir.KindSettle:
				s = other.Proposals
			default:
				s = other.Candidates
			}
			for _, id := range l.handleFor(other, s).ids() {
				if ids[id] {
					catches = append(catches, other)
					break
				}
			}
		}
		if len(catches) > 0 {
			continue
		}
		out = append(out, diag(CodeWF024, n.Path+".on_conflict",
			"on_conflict: skip leaves the conflicting proposals open and no later settle catches them: name a second settle over %s or choose on_conflict: stop or escalate",
			n.Proposals.From.String()))
	}
	return out
}

// -------------------------------------------------------------------- WF025

// openAtExit checks WF025, as far as the v1 vocabulary lets it be checked.
//
// The plan asks for an explicit `on_abort` wherever a proposal may remain
// open. There is no such field: spec.Document has no on_abort, no node has
// one, and the executor's abort-time disposition is scheduled with bead
// yaah-0u0v.11 — so there is no declaration here to read and no way for any
// document to satisfy a rule written against one. Inventing the field would
// make every real workflow fail a check for a key the decoder refuses as
// unknown (spec's strict decode is WF001), which is worse than silent.
//
// What is provable now is exactly the exposure the field exists to cover: a
// run that can reach the end of the graph with proposals still undecided. One
// finding names the whole set, because the subject is the run and not the
// proposal — WF021 already reports each proposal on its own at its producer —
// and because the CLI's exit message is the thing this has to serve: "this
// workflow can finish with N proposals open". When on_abort lands, the
// declaration discharges the finding and the rule keeps its number.
func openAtExit(l *linearity) []Diagnostic {
	var open []string
	for _, id := range l.tokensEverywhere() {
		if !l.claimed[id] {
			continue // the same plan gate WF021 uses: same subject, same set
		}
		if l.stranded[l.byPath(id)] {
			continue // WF021 names the container fix
		}
		if s, ok := l.runExit[id]; ok && s.counts&countNone != 0 {
			open = append(open, id)
		}
	}
	if len(open) == 0 {
		return nil
	}
	const maxNamed = 4
	shown := open
	if len(shown) > maxNamed {
		shown = append(append([]string{}, shown[:maxNamed]...), fmt.Sprintf("+%d more", len(shown)-maxNamed))
	}
	return []Diagnostic{diag(CodeWF025, "spec",
		"run can end with %s still open (%s); the vocabulary declares no on_abort disposition for them",
		proposalCount(len(open)), strings.Join(shown, ", "))}
}

// proposalCount renders the count with the noun agreeing, so a message reads
// "1 proposal" and "3 proposals" without a parenthetical for the reader to
// parse.
func proposalCount(n int) string {
	if n == 1 {
		return "1 proposal"
	}
	return fmt.Sprintf("%d proposals", n)
}

// -------------------------------------------------------------------- WF027

// continuationTargets checks WF027: continue_from and fork_from target a
// worktree proposal from the same role and lane, and a fork leaves its source
// settle-able.
//
// The two verbs differ in one way that decides a proposal's fate, and it is the
// harness's own: continue supersedes its source, so the parent is closed by
// starting the continuation and must not be settled afterwards; fork keeps its
// source open beside the new one, so the source still owes a disposition of its
// own. Both readings are implemented in the analysis — consumed tokens are
// counted as disposed at the continuing node, fork sources are left alone —
// and this rule checks the preconditions the harness will refuse at run time:
//
//   - the target names exactly one proposal-producing step, because a template
//     that interpolates two different steps' ids names no worktree this graph
//     can reason about, and one that names a report-lane dispatch names a
//     proposal that was never retained;
//   - same lane, since forking or continuing across lanes asks the harness for
//     a worktree the source run does not have;
//   - same role, since a continuation inherits the source session's
//     conversation and workspace, which belong to the role that made them;
//   - and for fork_from, that the source is disposed of downstream: not by
//     the fork, which does not close it. That half duplicates WF021's leak at
//     the source node, deliberately and in WF003-against-WF004's shape,
//     because the two fixes are different — settle the source, or mean
//     continue_from instead — and an author who wrote the wrong verb has to be
//     told at the verb.
func continuationTargets(l *linearity) []Diagnostic {
	var out []Diagnostic
	for _, n := range l.order {
		if n.Kind != ir.KindDelegate && n.Kind != ir.KindLoop {
			continue
		}
		for _, f := range []struct {
			key  string
			tpl  *ir.Template
			fork bool
		}{
			{"continue_from", n.ContinueFrom, false},
			{"fork_from", n.ForkFrom, true},
		} {
			if f.tpl == nil {
				continue
			}
			at := n.Path + "." + f.key
			src := templateSource(f.tpl)
			if src == nil {
				out = append(out, diag(CodeWF027, at,
					"must name the output of exactly one proposal-producing step; a %s that reads no step, or two different ones, names no worktree proposal", f.key))
				continue
			}
			set := l.handleFor(n, selectorOfSource(src))
			producers := set.producers()
			switch {
			case len(producers) != 1:
				if reports := set.reports(); len(reports) > 0 {
					out = append(out, diag(CodeWF027, at,
						"names %s, a report-lane dispatch that retains no proposal to %s", reports[0].Path, verbOf(f.key)))
					continue
				}
				if !set.known {
					// A param or an item binding: the id may well be a real
					// proposal, and nothing in this graph can say which lane
					// it came from. The rule declines rather than guessing.
					continue
				}
				out = append(out, diag(CodeWF027, at,
					"names %s, which yields no proposal to %s", set.String(), verbOf(f.key)))
				continue
			}
			srcNode := producers[0]
			if role, lane, ok := l.effectiveDispatch(n); ok {
				if lane != "" && srcNode.Lane != "" && lane != srcNode.Lane {
					out = append(out, diag(CodeWF027, at,
						"%ss across lanes: this step is %s and %s is %s, so the source worktree is not the one this dispatch would continue",
						verbOf(f.key), lane, srcNode.Path, srcNode.Lane))
					continue
				}
				if role != "" && srcNode.Role != "" && role != srcNode.Role {
					out = append(out, diag(CodeWF027, at,
						"%ss a proposal from another role: this step is role %s and %s is role %s, and a continuation inherits the source session's conversation",
						verbOf(f.key), quote(role), srcNode.Path, quote(srcNode.Role)))
					continue
				}
			}
			if !f.fork {
				continue
			}
			// fork_from leaves the source open. If nothing disposes of it on
			// the paths out of here, the author has written a fork where the
			// harness's consume-on-continue was meant.
			if s, ok := l.runExit[srcNode.Path]; ok && s.counts&countNone != 0 {
				out = append(out, diag(CodeWF027, at,
					"does not consume its source: %s stays open and no settle disposes of it, so this fork leaks a proposal — use continue_from to supersede it or settle %s", srcNode.Path, srcNode.Path))
			}
		}
	}
	return out
}

// effectiveDispatch is the role and lane a continue or fork is written under.
// A delegate states both itself. A loop declares neither, and what it repeats
// is its body's dispatch, so the body's single proposal producer answers —
// which is the pair the harness actually compares, and the reason bounded
// repair's `continue_from: steps.implement` on a loop whose body dispatches the
// same developer role is legal. A body with two producers has no single answer
// and the check declines.
func (l *linearity) effectiveDispatch(n *ir.Node) (role string, lane ir.Lane, ok bool) {
	if n.Kind == ir.KindDelegate {
		return n.Role, n.Lane, true
	}
	var props []*ir.Node
	for _, b := range n.Body {
		if b != nil && produces(b) {
			props = append(props, b)
		}
	}
	if len(props) != 1 {
		return "", "", false
	}
	return props[0].Role, props[0].Lane, true
}

func verbOf(key string) string {
	if key == "fork_from" {
		return "fork"
	}
	return "continue"
}

// ------------------------------------------------------------------ the proof

// SettlementProof is the linearity artifact: one disposition record per
// proposal the graph can create. It is the same fixed point the rules read,
// published for `yaah workflow explain` (bead yaah-0u0v.9) and for the
// property test that keeps the two honest — a workflow the family accepts must
// have a proof in which every proposal is accounted for exactly once.
type SettlementProof struct {
	// Proposals lists one record per token, in document order of the node
	// that produces it.
	Proposals []ProposalProof
	// OpenAtExit names the proposals a run can finish without disposing of,
	// in the same order.
	OpenAtExit []string
}

// ProposalProof is one proposal's disposition record.
type ProposalProof struct {
	// Proposal is the producing node's path, which is the token's identity.
	Proposal string
	// Dispositions names the steps that settle it — a settlement step, a
	// select that decides it, or a dispatch that continues it — in sorted
	// node-path order. More than one entry does not by itself break
	// linearity: two alternatives in different branch arms each settle it
	// once. OnceOnEveryPath is the property.
	Dispositions []string
	// Actions is the verbs applied to it, rendered by disposition.String.
	Actions string
	// OnceOnEveryPath reports that no reachable path applies a second
	// disposition.
	OnceOnEveryPath bool
	// SettledOnEveryPath reports that no reachable path leaves it undisposed.
	SettledOnEveryPath bool
	// Stranded reports a proposal sealed inside a body that declares no
	// collect, which no step outside can name.
	Stranded bool
}

// Prove runs the linearity analysis and returns the settlement proof for a
// resolved graph. A nil graph produces no proof.
func Prove(g *ir.Graph) *SettlementProof {
	if g == nil {
		return nil
	}
	return analyze(g).proof()
}

func (l *linearity) proof() *SettlementProof {
	p := &SettlementProof{}
	for _, id := range l.tokensEverywhere() {
		node := l.byPath(id)
		rec := ProposalProof{Proposal: id, Actions: "undisposed", Stranded: node != nil && l.stranded[node]}
		var names []string
		for _, s := range l.settledBy[id] {
			names = append(names, s.Path)
		}
		for _, s := range l.consumedBy[id] {
			names = append(names, s.Path)
		}
		sort.Strings(names)
		rec.Dispositions = dedupe(names)
		if exit, ok := l.runExit[id]; ok {
			rec.Actions = exit.acts.String()
			rec.OnceOnEveryPath = exit.counts&countTwice == 0
			rec.SettledOnEveryPath = exit.counts&countNone == 0
		} else if s, ok := l.seen[id]; ok {
			rec.Actions = s.acts.String()
			rec.OnceOnEveryPath = s.counts&countTwice == 0
			// A token that never reaches a run end is not settled on every
			// path; it is not reachable from one at all, which is WF021's
			// stranded finding.
			rec.SettledOnEveryPath = false
		}
		if !rec.SettledOnEveryPath && node != nil {
			p.OpenAtExit = append(p.OpenAtExit, id)
		}
		p.Proposals = append(p.Proposals, rec)
	}
	return p
}

// String renders one record as the line a report prints: the proposal, the
// verbs applied to it, and where they came from.
func (p ProposalProof) String() string {
	state := "settled-once"
	switch {
	case p.Stranded:
		state = "stranded"
	case !p.SettledOnEveryPath:
		state = "open"
	case !p.OnceOnEveryPath:
		state = "settled-twice"
	}
	return p.Proposal + " " + state + " " + p.Actions + " by [" + strings.Join(p.Dispositions, " ") + "]"
}

func dedupe(in []string) []string {
	out := in[:0:0]
	for i, s := range in {
		if i > 0 && in[i-1] == s {
			continue
		}
		out = append(out, s)
	}
	return out
}
