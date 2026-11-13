package validate

// Proposal linearity is a dataflow analysis, not a second implementation of
// the executor. Every rule in the family answers one question this file
// settles for the whole graph: for each proposal a workflow can create, how
// many dispositions can a run apply to it, and can any run finish with it
// still open.
//
// The unit is a token: one proposal-producing node, keyed by that node's
// path. A proposal-lane dispatch yields a proposal every time it runs, so a
// fan-out body's delegate is one token produced once per item and a loop
// body's delegate is one token produced up to max_iterations times. The
// instance count is deliberately not modelled: a settlement step is written
// over the set (`steps.x.collect`) or over the single value
// (`steps.x.output`), so a settle that covers the token covers every
// instance of it, and collapsing instances is what keeps a repeated body
// checkable at all. The approximations that follow from the collapse are
// listed on Linearity.
//
// A token travels a control-flow relation rebuilt from the graph, and each
// part of it is read the way the scheduler reads it:
//
//   - needs orders two steps and joins on all of them — WF004's semantics,
//     reused because checking against an order the runtime will not perform
//     proves nothing;
//   - a reference is a data dependency. `proposals: {from: steps.build.collect}`
//     means the settle runs after the fan-out whether or not the author also
//     wrote the needs entry, so WF023 must not call those two steps parallel;
//   - branch arms and gate dispositions are alternative successors, joined
//     by union: that is what turns a proposal settled in one arm only into a
//     leak on the other;
//   - inside a body, nodes run in the order written, and a body's proposals
//     leave it only through its container's collect.

import (
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// linearityState is the proof carried for one token across one node boundary.
//
// counts is a may-set of how many dispositions a run reaching the boundary
// has already applied: countNone for the runs that have not settled it,
// countOnce and countTwice for the runs that have. Keeping the three as bits
// rather than as a number is what makes this a may analysis over a finite
// lattice: a merge ORs the bits, the transfer shifts them, and a graph whose
// references happen to form a cycle still stops.
type linearityState struct {
	counts uint8
	// acts is the set of verbs applied along the runs counts describes.
	// WF022 reads it: a proposal some path already released must not be an
	// apply target further down.
	acts disposition
	// by names the nodes that applied a disposition on at least one path,
	// sorted and deduplicated so two runs over one graph agree byte for byte.
	by []string
}

// Disposition counts held by one token at one boundary.
const (
	countNone = 1 << iota
	countOnce
	countTwice
)

// disposition names a settlement verb, plus the one lifecycle verb the
// analysis must count: a continue_from supersedes its source, which disposes
// of it in exactly the sense WF021 means — the harness closes the parent when
// it starts the continuation.
type disposition uint8

// Disposition verbs.
const (
	dispApply disposition = 1 << iota
	dispSelect
	dispRelease
	dispDiscard
	dispSupersede
)

func (d disposition) String() string {
	var out []string
	for _, one := range []struct {
		bit  disposition
		name string
	}{
		{dispApply, "apply"},
		{dispSelect, "select"},
		{dispRelease, "release"},
		{dispDiscard, "discard"},
		{dispSupersede, "continue_from"},
	} {
		if d&one.bit != 0 {
			out = append(out, one.name)
		}
	}
	if len(out) == 0 {
		return "undisposed"
	}
	return strings.Join(out, "/")
}

// actionOf maps a settlement verb onto its disposition bit.
func actionOf(a ir.SettleAction) disposition {
	switch a {
	case ir.SettleApply:
		return dispApply
	case ir.SettleSelect:
		return dispSelect
	case ir.SettleRelease:
		return dispRelease
	case ir.SettleDiscard:
		return dispDiscard
	}
	return 0
}

// freshState is a token that exists and has no disposition yet.
func freshState() linearityState {
	return linearityState{counts: countNone}
}

// mergeState unions two boundary states: a run reaching the boundary came
// either way, so counts, verbs, and disposer names all join by union.
func mergeState(a, b linearityState) linearityState {
	out := linearityState{counts: a.counts | b.counts, acts: a.acts | b.acts}
	out.by = unionNames(a.by, b.by)
	return out
}

// namedBy returns the state after one step disposes of the token. The shift
// is the whole of the exactly-once bookkeeping: an undisposed token becomes
// disposed-once, and a token already disposed on some path becomes disposed
// twice on that path — which is the finding WF021 reports at the second
// disposer, because that is the step the author has to narrow or delete.
//
// Re-applying the same node's disposition is a no-op. Kleene rounds re-run a
// transfer on a grown entry, and without the guard a settle inside a loop
// body would shift its own token from once to twice on the second round and
// report a double settlement no run performs.
func namedBy(s linearityState, at string, act disposition) linearityState {
	if len(s.by) == 1 && s.by[0] == at {
		return s
	}
	out := linearityState{acts: s.acts | act, by: unionNames(s.by, []string{at})}
	if s.counts&countNone != 0 {
		out.counts |= countOnce
	}
	if s.counts&(countOnce|countTwice) != 0 {
		out.counts |= countTwice
	}
	if out.counts == 0 {
		// A state that arrives with no history at all: the token was named
		// by this step without an earlier production point on the path the
		// analysis walked, which is a first disposition, not a second one.
		out.counts = countOnce
	}
	return out
}

func unionNames(a, b []string) []string {
	if len(a) == 0 {
		return append([]string{}, b...)
	}
	if len(b) == 0 {
		return append([]string{}, a...)
	}
	out := make([]string, 0, len(a)+len(b))
	seen := map[string]bool{}
	for _, s := range append(append([]string{}, a...), b...) {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// produces reports whether a node yields a proposal token. A proposal-lane
// dispatch is the obvious case, and a lane the author left unset counts too:
// resolve folds defaults, and an unset lane is the runtime's default lane —
// the same reading WF020 takes, and the one that keeps an unlabeled workstream
// a proposal. A select is the other producer: it hands on one candidate as the
// winner, and that winner is a proposal with a disposition still owed to it,
// under a fresh identity because the selection itself decides the candidates
// it rejected.
func produces(n *ir.Node) bool {
	switch n.Kind {
	case ir.KindDelegate:
		return n.Lane != ir.LaneReport
	case ir.KindSelect:
		return true
	}
	return false
}

// proposalSet is what a `settle.proposals` or `select.candidates` selector
// denotes, as far as one graph can say.
type proposalSet struct {
	// referents are the dispatch and selection nodes the handle names, in
	// document order.
	referents []*ir.Node
	// attributed is false for a handle the graph cannot trace to a producer —
	// a parameter or a fan-out item — which no linearity rule counts. An
	// attributed handle with no referents resolved to a step at all.
	attributed bool
	// known is false when the handle names no step it can attribute a lane
	// to: a param, an item, or a reference that resolves to nothing.
	known bool
	// singleton says the handle denotes at most one proposal, so two of its
	// elements cannot conflict.
	singleton bool
	// maybeEmpty says the handle can denote no proposals at all, which is
	// what makes `on_empty` a live decision.
	maybeEmpty bool
	// provablyEmpty says the document states the set has no elements at all:
	// an empty literal fan-out source, or an empty list default on a
	// parameter. This is stronger than maybeEmpty and is WF028's trigger.
	provablyEmpty bool
}

// producers is the subset of the referents that yield a proposal.
func (p proposalSet) producers() []*ir.Node {
	var out []*ir.Node
	for _, n := range p.referents {
		if produces(n) {
			out = append(out, n)
		}
	}
	return out
}

// reports is the subset that provably cannot carry a proposal: a dispatch the
// author labeled report lane. An unlabeled lane is the default lane and is not
// evidence of anything, which is why this is narrower than "not a producer".
func (p proposalSet) reports() []*ir.Node {
	var out []*ir.Node
	for _, n := range p.referents {
		if n.Kind == ir.KindDelegate && n.Lane == ir.LaneReport {
			out = append(out, n)
		}
	}
	return out
}

// ids are the token paths the handle denotes.
func (p proposalSet) ids() []string {
	var out []string
	for _, n := range p.producers() {
		out = append(out, n.Path)
	}
	return out
}

// String renders the handle's referents for a message, bounded so a wide
// collect cannot bury the finding.
func (p proposalSet) String() string {
	if len(p.referents) == 0 {
		return "no dispatch this graph can attribute"
	}
	names := make([]string, 0, len(p.referents))
	for _, n := range p.referents {
		names = append(names, n.Path)
	}
	if len(names) > 3 {
		names = append(names[:3], "…")
	}
	return strings.Join(names, ", ")
}

// linearity is one analysis of one graph: the flow relation, the boundary
// states, and the residue the exit rules read. Nothing here survives the call
// — a validator holds no state between graphs.
type linearity struct {
	g      *ir.Graph
	order  []*ir.Node
	parent map[*ir.Node]*ir.Node
	top    map[string]*ir.Node
	// scope is a node's visible step ids: every top-level step, plus the
	// body-local ids of every container it sits in up to and including the
	// node itself, which is the namespace ir.resolveBody compiled its
	// references against.
	scope map[*ir.Node]map[string]*ir.Node

	// preds and succs hold external edges only — needs, data reads, arm
	// targets. Container-to-body and sibling order are structural and are
	// walked by compute, so a body node never appears in another
	// container's predecessor list.
	preds map[*ir.Node][]*ir.Node
	succs map[*ir.Node][]*ir.Node
	// runEnds are the boundaries a run can finish at.
	runEnds []*ir.Node

	entry map[*ir.Node]map[string]linearityState
	exit  map[*ir.Node]map[string]linearityState
	// runExit merges every run end: the exit state WF021 and WF025 read.
	runExit map[string]linearityState
	// seen accumulates every token the analysis met, for the proof artifact.
	seen map[string]linearityState
	// stranded records a proposal produced in a body whose container declares
	// no collect: no step outside can ever name it, so no settle can dispose
	// of it and the run cannot end any other way.
	stranded map[*ir.Node]bool

	// settledBy indexes the settle and select nodes that name each token and
	// consumedBy the continue_from nodes that supersede it, both in document
	// order — the shape the pairwise rules query without re-walking
	// selectors.
	settledBy  map[string][]*ir.Node
	consumedBy map[string][]*ir.Node
	// claimed marks the tokens whose existence as proposals the document
	// itself states, which is the set the leak rules may speak about.
	claimed map[string]bool
	// guarded marks the nodes that sit behind a branch arm or a gate
	// disposition, whose state therefore describes an alternative path rather
	// than a step the scheduler waited for.
	guarded map[*ir.Node]bool
}

// analyze builds the flow relation and runs the dataflow to a fixed point.
func analyze(g *ir.Graph) *linearity {
	l := &linearity{
		g:          g,
		top:        stepsByID(g),
		scope:      map[*ir.Node]map[string]*ir.Node{},
		preds:      map[*ir.Node][]*ir.Node{},
		succs:      map[*ir.Node][]*ir.Node{},
		entry:      map[*ir.Node]map[string]linearityState{},
		exit:       map[*ir.Node]map[string]linearityState{},
		seen:       map[string]linearityState{},
		stranded:   map[*ir.Node]bool{},
		settledBy:  map[string][]*ir.Node{},
		consumedBy: map[string][]*ir.Node{},
		claimed:    map[string]bool{},
		guarded:    map[*ir.Node]bool{},
	}
	l.order, l.parent = graphNodes(g)
	l.build()
	l.propagate()
	l.indexDispositions()
	l.claimScope()
	return l
}

// ---------------------------------------------------------------- the graph

// build lays out the external flow edges.
func (l *linearity) build() {
	for _, n := range l.order {
		for _, ref := range n.Needs {
			if dep, ok := l.top[ref]; ok && dep != n {
				l.edge(dep, n)
			}
		}
		for _, src := range l.reads(n) {
			dep := l.resolved(n, src)
			if dep == nil || dep == n {
				continue
			}
			l.edge(dep, n)
		}
		if n.Kind == ir.KindBranch || n.Kind == ir.KindGate {
			for _, arm := range l.arms(n) {
				l.edge(n, arm)
			}
		}
	}
	// A run finishes where the flow has nowhere left to go. Containers count
	// at their own boundary, because compute has already carried a body's
	// state out through them.
	for _, n := range l.g.Steps {
		if n == nil {
			continue
		}
		if len(l.succs[n]) == 0 {
			l.runEnds = append(l.runEnds, n)
			continue
		}
		// An arm set that cannot cover every outcome also ends the flow: a
		// branch with no default and a gate missing a disposition both have a
		// case where no arm runs, and that is the run in which a proposal
		// settled only inside an arm stays open.
		if len(l.arms(n)) > 0 && !l.armsTotal(n) {
			l.runEnds = append(l.runEnds, n)
		}
	}
}

// edge records one external flow edge in both directions, deduplicated.
//
// Both endpoints are lifted to their top-level step. A body node is entered
// through its container and sees the state the container's own transfer
// produced — a loop's continue_from, for one — so an edge that stopped at the
// body node would hand it a predecessor's state that had not passed through
// the container, and a proposal the container superseded would arrive there
// still open. Needs and arm targets already name top-level steps only
// (ir.resolveNode and resolveBranch enforce it); a data reference from inside
// a body may name one, and that is the case the lift is for.
func (l *linearity) edge(from, to *ir.Node) {
	from, to = l.flow(from), l.flow(to)
	if from == to || from == nil || to == nil {
		return
	}
	for _, n := range l.preds[to] {
		if n == from {
			return
		}
	}
	l.preds[to] = append(l.preds[to], from)
	l.succs[from] = append(l.succs[from], to)
}

// flow is the step a node's flow is attributed to: the top-level step that
// contains it, or the node itself. Everything the family says about ordering,
// concurrency, and where a run can end is a statement about top-level steps,
// because a body has no independent place in the schedule.
func (l *linearity) flow(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	for l.parent[n] != nil {
		n = l.parent[n]
	}
	return n
}

// arms are a branch's or gate's alternative successors, resolved to nodes.
func (l *linearity) arms(n *ir.Node) []*ir.Node {
	var out []*ir.Node
	add := func(id string) {
		if id == "" {
			return
		}
		if t, ok := l.top[id]; ok {
			out = append(out, t)
		}
	}
	for _, c := range n.Cases {
		add(c.Then)
	}
	add(n.Default)
	add(n.OnPass)
	add(n.OnFail)
	return out
}

// armsTotal reports whether the declared arms cover every outcome: a branch
// with a default, a gate with both dispositions.
func (l *linearity) armsTotal(n *ir.Node) bool {
	switch n.Kind {
	case ir.KindBranch:
		return n.Default != ""
	case ir.KindGate:
		return n.OnPass != "" && n.OnFail != ""
	}
	return true
}

// reads returns every step source the node's own values name, deduplicated.
// Both halves of the value surface count: selectors, which are the graph's
// read-side, and compiled template references, which is how a prompt or a
// continue_from reaches back at a step.
func (l *linearity) reads(n *ir.Node) []*ir.Source {
	var out []*ir.Source
	add := func(src *ir.Source) {
		if src == nil {
			return
		}
		if src.Kind != ir.SourceStepOutput && src.Kind != ir.SourceStepCollect {
			return
		}
		for _, seen := range out {
			if seen.Kind == src.Kind && seen.StepID == src.StepID {
				return
			}
		}
		out = append(out, src)
	}
	sel := func(s *ir.Selector) {
		if s != nil {
			add(s.From)
		}
	}
	tpl := func(t *ir.Template) {
		if t == nil {
			return
		}
		for _, r := range t.Refs {
			add(sourceOfRef(r))
		}
		for _, seg := range t.Segments {
			add(sourceOfRef(seg.Ref))
		}
	}
	sel(n.When)
	sel(n.On)
	sel(n.Until)
	if n.Over != nil {
		sel(n.Over.Selector)
	}
	sel(n.Proposals)
	sel(n.Where)
	sel(n.Candidates)
	for _, s := range n.Inputs {
		sel(s)
	}
	tpl(n.Prompt)
	tpl(n.SeedFrom)
	tpl(n.ContinueFrom)
	tpl(n.ForkFrom)
	tpl(n.Value)
	if n.Judge != nil {
		tpl(n.Judge.Prompt)
	}
	for _, k := range sortedParamKeys(n.CallParams) {
		tpl(n.CallParams[k].Template)
	}
	return out
}

// resolved looks one step source up in the scope of the node that holds it.
func (l *linearity) resolved(n *ir.Node, src *ir.Source) *ir.Node {
	if src == nil {
		return nil
	}
	return l.scopeOf(n)[src.StepID]
}

// scopeOf is the reference namespace one node resolves against: every
// top-level step id, plus the body-local ids of every container it sits in,
// up to and including itself, exactly as ir.resolveBody registers them. This
// family rebuilds it rather than borrowing the value family's copy because
// every rule file here owns its own traversal.
func (l *linearity) scopeOf(n *ir.Node) map[string]*ir.Node {
	if s, ok := l.scope[n]; ok {
		return s
	}
	s := map[string]*ir.Node{}
	for id, node := range l.top {
		s[id] = node
	}
	for child := n; ; {
		up := l.parent[child]
		if up == nil {
			break
		}
		for _, sib := range up.Body {
			if sib == nil || sib.ID == "" {
				continue
			}
			s[sib.ID] = sib
			if sib == child {
				break
			}
		}
		child = up
	}
	l.scope[n] = s
	return s
}

// --------------------------------------------------------------- dataflow

// linearityRounds bounds the Kleene iteration. The flow relation is a DAG for
// any document the earlier families accept — needs is acyclic (WF003) and a
// node may not read its own output (WF013) — so one pass in document order
// converges, and the bound only matters for a hand-edited graph whose
// backward references close a cycle: the walk stops there instead of spinning.
func (l *linearity) rounds() int {
	if len(l.order) < 2 {
		return 2
	}
	return len(l.order) + 1
}

// propagate evaluates every node, container subtrees included, until the
// boundary states stop growing.
func (l *linearity) propagate() {
	l.markGuarded()
	for i := 0; i < l.rounds(); i++ {
		changed := false
		for _, n := range l.g.Steps {
			if n == nil {
				continue
			}
			if l.compute(n, nil) {
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	l.runExit = map[string]linearityState{}
	for _, n := range l.runEnds {
		mergeInto(l.runExit, l.exitState(n))
	}
	l.seen = map[string]linearityState{}
	for _, n := range l.order {
		for id, s := range l.exit[n] {
			l.seen[id] = mergeState(l.seen[id], s)
		}
	}
	for id, s := range l.runExit {
		l.seen[id] = mergeState(l.seen[id], s)
	}
}

// markGuarded records which nodes sit behind a branch arm or a gate
// disposition. The flag decides how a node's predecessors are combined, which
// is the difference between two readings of the same graph that only look
// right in one of the two cases: needs and data dependencies are *joins* — the
// scheduler waits for every one of them, so all of them ran, and a proposal
// settled by the later one cannot still be open at this node — while anything
// downstream of a guard is an *alternative*, so a proposal settled only in one
// arm really is left open on the path that took the other.
//
// The mark spreads to the whole downstream cone of an arm, which is the
// conservative direction: a guarded graph falls back to the may-union it would
// have had anyway, and only an unguarded chain gets the join that stops a
// settle's own state from being diluted by an ancestor it already waited for.
func (l *linearity) markGuarded() {
	work := []*ir.Node{}
	for _, n := range l.order {
		if n.Kind != ir.KindBranch && n.Kind != ir.KindGate {
			continue
		}
		for _, arm := range l.arms(n) {
			if !l.guarded[arm] {
				l.guarded[arm] = true
				work = append(work, arm)
			}
		}
	}
	for len(work) > 0 {
		n := work[len(work)-1]
		work = work[:len(work)-1]
		for _, s := range l.succs[n] {
			if !l.guarded[s] {
				l.guarded[s] = true
				work = append(work, s)
			}
		}
	}
}

// joinDefinite combines the state of a step the scheduler waited for. Both
// steps finished, so for each token the more advanced of the two readings wins:
// a proposal disposed of by either one is disposed of here. Verbs and disposer
// names still union, because both really happened.
func joinDefinite(dst map[string]linearityState, src map[string]linearityState) {
	for id, s := range src {
		cur := dst[id]
		out := mergeState(cur, s)
		out.counts = advanced(cur.counts, s.counts)
		dst[id] = out
	}
}

// advanced is the join of two disposition counts for two steps that both ran:
// any advancement beats "not yet", and a second advancement stays visible.
func advanced(a, b uint8) uint8 {
	if out := (a | b) & (countOnce | countTwice); out != 0 {
		return out
	}
	return countNone
}

// compute evaluates one node and, if it is a container, its whole body. in is
// the state arriving from the previous sibling in the same body, nil at the
// top of a container or at the start of a body. It reports whether any
// boundary state grew, which is what tells the caller the walk has settled.
func (l *linearity) compute(n *ir.Node, in map[string]linearityState) bool {
	entry := map[string]linearityState{}
	guarded := false
	for _, p := range l.preds[n] {
		if l.guarded[p] {
			guarded = true
			break
		}
	}
	if in != nil {
		if guarded {
			mergeInto(entry, in)
		} else {
			joinDefinite(entry, in)
		}
	}
	for _, p := range l.preds[n] {
		if guarded {
			mergeInto(entry, l.exitState(p))
			continue
		}
		joinDefinite(entry, l.exitState(p))
	}
	down := l.transfer(n, entry)
	out := down
	if len(n.Body) > 0 {
		prev := down
		for _, b := range n.Body {
			if b == nil {
				continue
			}
			l.compute(b, prev)
			prev = l.exit[b]
		}
		out = l.gate(n, prev)
	}
	l.entry[n] = entry
	changed := !sameState(l.exit[n], out)
	l.exit[n] = out
	return changed
}

// exitState is the state a node hands to whoever runs after it.
func (l *linearity) exitState(n *ir.Node) map[string]linearityState {
	if s := l.exit[n]; s != nil {
		return s
	}
	return map[string]linearityState{}
}

// gate is the container boundary. A body's proposals leave it through the
// container's collect and nowhere else, so a container that declares none
// closes over them: the tokens stop here, and any that arrived undisposed at
// this boundary can never be disposed anywhere, because no handle outside can
// name them. That is recorded as stranded for WF021 rather than passed on as
// an open token the exit rules would have no path to attribute.
func (l *linearity) gate(container *ir.Node, bodyEnd map[string]linearityState) map[string]linearityState {
	if container.Collect != "" || bodyEnd == nil {
		return bodyEnd
	}
	out := map[string]linearityState{}
	for id, s := range bodyEnd {
		node := l.byPath(id)
		if node != nil && l.inside(node, container) {
			if s.counts&countNone != 0 {
				l.stranded[node] = true
			}
			continue
		}
		out[id] = s
	}
	return out
}

// transfer computes one node's exit state from its entry.
func (l *linearity) transfer(n *ir.Node, in map[string]linearityState) map[string]linearityState {
	out := cloneState(in)
	switch n.Kind {
	case ir.KindSettle:
		act := actionOf(n.Action)
		for _, id := range l.handleFor(n, n.Proposals).ids() {
			out[id] = namedBy(out[id], n.Path, act)
		}
	case ir.KindSelect:
		// A selection decides its candidates: the rejected ones are not open
		// proposals any more, and the winner is a new one. The reading is a
		// decision, not a deduction, and it is the one the vocabulary
		// implies — a judge that disposed nothing would make every speculative
		// workflow leak each candidate it did not pick, which is not what a
		// pick is for. The winner still owes its own disposition.
		for _, id := range l.handleFor(n, n.Candidates).ids() {
			out[id] = namedBy(out[id], n.Path, dispSelect)
		}
	case ir.KindLoop, ir.KindDelegate:
		if id := l.consumed(n); id != "" {
			out[id] = namedBy(out[id], n.Path, dispSupersede)
		}
	}
	if produces(n) {
		// Each execution mints a fresh proposal, so the token's own history
		// restarts here: a loop-carried disposition belongs to a previous
		// instance, and counting it against this one would report a double
		// settlement the run never performs.
		out[n.Path] = freshState()
	}
	if n.When != nil {
		// The guard can be false, and then the node does nothing at all and
		// its entry flows straight out. Keeping that alternative is what lets
		// WF021 see a proposal whose only settle was skipped.
		for id, s := range in {
			out[id] = mergeState(out[id], s)
		}
	}
	return out
}

// consumed is the token a continue_from supersedes. Only continue_from
// consumes: fork_from leaves its source settle-able, which is WF027's whole
// point, and seed_from threads a conversation without touching a proposal's
// disposition at all. A template that names nothing, or more than one
// proposal, is WF027's finding and is not counted here.
func (l *linearity) consumed(n *ir.Node) string {
	if n.ContinueFrom == nil {
		return ""
	}
	if ps := l.handleFor(n, selectorOfSource(templateSource(n.ContinueFrom))).producers(); len(ps) == 1 {
		return ps[0].Path
	}
	return ""
}

// indexDispositions records which steps name each token.
func (l *linearity) indexDispositions() {
	for _, n := range l.order {
		switch n.Kind {
		case ir.KindSettle:
			for _, id := range l.handleFor(n, n.Proposals).ids() {
				l.settledBy[id] = append(l.settledBy[id], n)
			}
		case ir.KindSelect:
			for _, id := range l.handleFor(n, n.Candidates).ids() {
				l.settledBy[id] = append(l.settledBy[id], n)
			}
		case ir.KindLoop, ir.KindDelegate:
			if id := l.consumed(n); id != "" {
				l.consumedBy[id] = append(l.consumedBy[id], n)
			}
		}
	}
}

// claimScope decides which tokens the leak rules may call a proposal. The
// linearity family's subject is a document's settlement plan, so a token is
// in plan when the author put it there one of four ways: a handle names it
// (a settle, a selection, or a continuation already reaches for it, which is
// the strongest statement available), the dispatch declares the proposal lane
// outright, the dispatch types its output as a Proposal, or the token is the
// winner of a select whose candidates are themselves in plan.
//
// The gate is what keeps this family honest about the graphs it is handed.
// A delegate whose lane nobody wrote down is a dispatch on the runtime default
// lane, and the default lane is not a retention claim this package can read as
// one — WF011 is the rule that refuses a producer whose *declared* type
// contradicts a settle. So a workflow that never mentions settlement, which is
// most of the corpus and every fixture written for another family's rule, is
// not a workflow with a leak: it is a workflow with no settlement plan, and
// there is nothing here to prove. Without this gate WF021 reports a leak for
// every dispatch in the graph, which is a finding no author can act on and a
// rule that cannot be wired into Validate at all.
func (l *linearity) claimScope() {
	for _, n := range l.order {
		if !produces(n) {
			continue
		}
		switch {
		case len(l.settledBy[n.Path]) > 0 || len(l.consumedBy[n.Path]) > 0:
			l.claimed[n.Path] = true
		case n.Kind == ir.KindDelegate && n.Lane == ir.LaneProposal:
			l.claimed[n.Path] = true
		case n.Output != nil && n.Output.Type == ir.OutputProposal:
			l.claimed[n.Path] = true
		}
	}
	// A selection's winner inherits its candidates' plan: whoever writes a
	// judge over a set of proposals has started talking about settlement, and
	// the proposal it picks is the one that still needs landing.
	for _, n := range l.order {
		if n.Kind != ir.KindSelect || l.claimed[n.Path] {
			continue
		}
		for _, id := range l.handleFor(n, n.Candidates).ids() {
			if l.claimed[id] {
				l.claimed[n.Path] = true
				break
			}
		}
	}
}

// tokensEverywhere lists every token the analysis met, in document order of
// the node that produces it, so a rule and the proof artifact agree on order.
func (l *linearity) tokensEverywhere() []string {
	var out []string
	for _, n := range l.order {
		if _, ok := l.seen[n.Path]; ok {
			out = append(out, n.Path)
		}
	}
	var orphan []string
	for id := range l.seen {
		if l.byPath(id) == nil {
			orphan = append(orphan, id)
		}
	}
	// A token with no producer node in the graph cannot happen through
	// Resolve; it can only come from a hand-edited graph whose paths were
	// rewritten. Ordered after the producers so the artifact stays stable.
	sort.Strings(orphan)
	return append(out, orphan...)
}

// byPath finds a node by its resolved path; token ids are node paths.
func (l *linearity) byPath(path string) *ir.Node {
	for _, n := range l.order {
		if n.Path == path {
			return n
		}
	}
	return nil
}

// inside reports whether a node sits in container's body, at any depth.
func (l *linearity) inside(node, container *ir.Node) bool {
	for up := l.parent[node]; up != nil; up = l.parent[up] {
		if up == container {
			return true
		}
	}
	return false
}

// reachable reports whether a run can get from one node to another. The
// pairwise rules ask it of two settlement steps: with no path either way they
// may both execute, which is what makes their path intent a question (WF023),
// and with a path from a skip-prone settle on to another settle, the skipped
// set has somewhere to be caught (WF024). The answer is about top-level steps,
// so a settle inside a body is reached through its container, which is also
// how the sibling order inside that body reads: two settlements in one body
// are ordered, because the body is a sequence.
func (l *linearity) reachable(from, to *ir.Node) bool {
	from, to = l.flow(from), l.flow(to)
	if from == nil || to == nil {
		return false
	}
	if from == to {
		return true
	}
	seen := map[*ir.Node]bool{from: true}
	stack := []*ir.Node{from}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, s := range l.succs[n] {
			if s == to {
				return true
			}
			if !seen[s] {
				seen[s] = true
				stack = append(stack, s)
			}
		}
	}
	return false
}

// ------------------------------------------------------------ the handle set

// handleFor resolves a selector that should denote a set of proposals, as
// written by reader. Three things are decided here and each is a rule's whole
// premise:
//
//   - which dispatch nodes the handle names, which is WF020's lane evidence
//     and WF021's bookkeeping;
//   - whether it can denote nothing at all, which is WF026's: a filter, a
//     run-time-sized fan-out, an empty literal list, and a value the graph
//     cannot trace are all possibly empty, and `on_empty` is the author's
//     answer for each;
//   - whether it denotes one proposal, which is WF024's exemption: a set of
//     one has no two elements to conflict, so `on_conflict: skip` on it owes
//     no catch.
func (l *linearity) handleFor(reader *ir.Node, sel *ir.Selector) proposalSet {
	if sel == nil || sel.From == nil {
		return proposalSet{maybeEmpty: true}
	}
	// A filter is the plain statement that the set may come back empty, and a
	// field-only clause is a retention filter in the same shape (pinned on
	// ir.Predicate.Field): it keeps elements, it does not guarantee any.
	filtered := sel.Predicate.Op != ir.PredNone || sel.Predicate.Field != ""
	out := proposalSet{
		singleton:  sel.AsList,
		maybeEmpty: sel.AsList || filtered,
	}
	switch sel.From.Kind {
	case ir.SourceStepCollect, ir.SourceStepOutput:
		target := l.scopeOf(reader)[sel.From.StepID]
		if target == nil {
			// referencesResolve reports the unresolvable step; nothing here
			// can be said about the set it would have denoted.
			out.maybeEmpty = true
			return out
		}
		if sel.From.Kind == ir.SourceStepOutput {
			out.attributed = true
			out.known = true
			out.referents = []*ir.Node{target}
			if !sel.AsList {
				// One step's output is one value; read as a set it is either
				// a list that value holds or WF011's finding, and either way
				// its size is not knowable here.
				out.maybeEmpty = true
			}
			return out
		}
		if len(target.Body) == 0 {
			out.maybeEmpty = true
			return out
		}
		out.attributed = true
		out.known = true
		out.referents = l.collectReferents(target)
		switch {
		case target.Over != nil && target.Over.Selector != nil:
			// A run-time item list can be empty and is not one element.
			out.maybeEmpty = true
			out.singleton = false
		case target.Over != nil && target.Over.Literals != nil:
			out.maybeEmpty = out.maybeEmpty || len(target.Over.Literals) == 0
			out.provablyEmpty = len(target.Over.Literals) == 0
			out.singleton = !filtered && len(target.Over.Literals) <= 1
		default:
			// A loop, or a container with no source at all: the count of
			// items nobody wrote down is not this package's to assume.
			out.maybeEmpty = true
			out.singleton = false
		}
	default:
		// A parameter or an item binding carries no lane and no size for this
		// package to read. WF020 stays silent about it and WF021 has no token
		// to count: the graph did not produce those proposals. A declared
		// default is the one case the document states outright.
		out.maybeEmpty = true
		out.singleton = false
		out.provablyEmpty = emptyDefault(l.g, sel.From)
	}
	return out
}

// emptyDefault reports a parameter whose declared default is a list with no
// elements — the only set size a `params:` source can state.
func emptyDefault(g *ir.Graph, src *ir.Source) bool {
	if g == nil || src == nil || src.Kind != ir.SourceParams || len(src.Path) > 0 {
		return false
	}
	p, ok := g.Params[src.Name]
	if !p.HasDefault {
		return false
	}
	list, isList := p.Default.([]any)
	return ok && isList && len(list) == 0
}

// collectReferents lists the dispatch nodes a container's collect denotes:
// its body's dispatches, reaching through a nested container only when that
// container publishes a collect of its own — which is the only way an outer
// step can read a nested value back.
func (l *linearity) collectReferents(container *ir.Node) []*ir.Node {
	var out []*ir.Node
	var walk func(nodes []*ir.Node)
	walk = func(nodes []*ir.Node) {
		for _, n := range nodes {
			if n == nil {
				continue
			}
			switch n.Kind {
			case ir.KindDelegate, ir.KindSelect:
				out = append(out, n)
			default:
				if len(n.Body) > 0 && n.Collect != "" {
					walk(n.Body)
				}
			}
		}
	}
	walk(container.Body)
	return out
}

// templateSource is the single step a seed/continue/fork template reads a
// proposal id from. The template renders text, so what matters for linearity
// is the step it names, and interpolating around the reference does not change
// which proposal that is. A template with no step reference or with two
// different ones denotes no single proposal, and the caller reports it.
func templateSource(t *ir.Template) *ir.Source {
	if t == nil {
		return nil
	}
	var found *ir.Source
	add := func(src *ir.Source) {
		if src == nil || (src.Kind != ir.SourceStepOutput && src.Kind != ir.SourceStepCollect) {
			return
		}
		if found == nil {
			found = src
			return
		}
		if found.Kind != src.Kind || found.StepID != src.StepID {
			found = nil // ambiguous: two different steps
		}
	}
	for _, r := range t.Refs {
		add(sourceOfRef(r))
	}
	for _, seg := range t.Segments {
		if seg.Ref == nil {
			continue
		}
		add(sourceOfRef(seg.Ref))
	}
	return found
}

// selectorOfSource wraps a compiled source as the one-field selector
// handleFor reads, so a template reference and a `proposals:` selector go
// through the same resolution. The wrapper is a fresh value; the graph is not
// touched.
func selectorOfSource(src *ir.Source) *ir.Selector {
	if src == nil {
		return nil
	}
	return &ir.Selector{From: src}
}

// ------------------------------------------------------------- state utils

func cloneState(in map[string]linearityState) map[string]linearityState {
	out := make(map[string]linearityState, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeInto(dst map[string]linearityState, src map[string]linearityState) {
	for id, s := range src {
		dst[id] = mergeState(dst[id], s)
	}
}

func sameState(a, b map[string]linearityState) bool {
	if len(a) != len(b) {
		return false
	}
	for id, s := range a {
		o, ok := b[id]
		if !ok || o.counts != s.counts || o.acts != s.acts || strings.Join(o.by, ",") != strings.Join(s.by, ",") {
			return false
		}
	}
	return true
}
