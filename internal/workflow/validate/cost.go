package validate

import (
	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// costSaturate bounds the arithmetic behind a worst-case estimate. A graph
// is capped at spec.MaxNodes, but nested loops and fan-outs multiply, so
// 256 nodes of max_iterations 16 each would overflow an int on the way to
// a number no host would ever run. Totals saturate here and explain says
// the run is over any budget rather than printing a wrapped negative.
const costSaturate = 1 << 40

// Cost runs the cost half of the family — WF033, WF034, WF035 — over a
// resolved graph and returns every finding in code order, nodes in
// document order.
func Cost(g *ir.Graph, env Env) []Diagnostic {
	if g == nil {
		return nil
	}
	var out []Diagnostic
	out = append(out, dispatchBudget(g, env)...)
	out = append(out, repetitionBounds(g)...)
	out = append(out, configuredCaps(g, env)...)
	return out
}

// Explain is the seam for `yaah workflow explain` (bead yaah-0u0v.9): the
// whole family's findings beside the two artifacts a human needs before
// approving a permission plan — the authority union and the worst-case
// cost. It computes nothing that Authority, Cost, and the two summarisers
// do not, and it returns the findings in the order the two aggregates
// emit them, which is ascending within each half: WF030, WF031, WF032,
// WF036, then WF033, WF034, WF035. The split is by what a rule needs
// (host authority versus configured caps), not by code order, so the
// renderer sorts if it wants the codes contiguous.
func Explain(g *ir.Graph, env Env) ExplainResult {
	if g == nil {
		return ExplainResult{}
	}
	res := ExplainResult{Authority: authorityUnion(g, env), Cost: estimateCost(g, env)}
	res.Diagnostics = append(res.Diagnostics, Authority(g, env)...)
	res.Diagnostics = append(res.Diagnostics, Cost(g, env)...)
	return res
}

// ExplainResult is the explain artifact: diagnostics plus the two
// summaries, each computed over the same reachability as the rules that
// reference them.
type ExplainResult struct {
	Diagnostics []Diagnostic
	Authority   AuthorityUnion
	Cost        CostEstimate
}

// CostEstimate is the conservative dispatch, turn, and wall-clock bound for
// one run, plus the honesty flags a report needs: a total is only a
// ceiling when every repetition in the document is bounded.
type CostEstimate struct {
	// Dispatches is the worst-case sub-agent dispatch count over every
	// reachable node: fan-out items times enclosing loop iterations
	// times the node's own declared retry attempts, with each call
	// expanded by its declared max_depth.
	Dispatches int
	// Complete reports that Dispatches bounds the run from above. When it
	// is false, Dispatches is a floor: at least one fan-out resolves its
	// item list at run time or one call reaches a workflow this graph
	// cannot see, and no document value bounds them.
	Complete bool
	// Unbound lists the node paths whose repetition the estimate could
	// not bound, in document order.
	Unbound []string
	// Turns bounds the model turns the run may spend, and WallClock
	// seconds the elapsed cost. Each is reported only when Env prices
	// the axis: Turns needs MaxTurnsPerDispatch, WallClock needs
	// SecondsPerDispatch or a timeout on every reachable dispatch.
	Turns          int
	TurnsKnown     bool
	WallClock      int
	WallClockKnown bool
	// Ceiling is the dispatch bound the run must fit under — the
	// declared budgets.max_dispatches, lowered by the host ceiling when
	// both exist — and ExceedsCeiling says Dispatches is over it. Ceiling
	// is 0 with ExceedsCeiling false when neither the document nor the
	// host declares one, which WF033 reports as unchecked rather than as
	// passing; a declared budget of 0 is a real ceiling and is honoured as
	// one.
	Ceiling        int
	ExceedsCeiling bool
}

// dispatchBudget checks WF033: the run's worst-case dispatch count must fit
// the budget it declares, and the declaration may not sit above the host
// ceiling.
//
// Two orderings matter and both are conservative. When the document
// declares a budget and the host also has one, the smaller applies, so a
// workflow cannot plan work the host would refuse to run. And when the
// count is a floor rather than a ceiling, exceeding the budget is still a
// sound finding: if the smallest defensible number is already over, the
// real run is over too. Falling short of the budget on an incomplete count
// proves nothing, so that combination stays silent and Explain's
// Unbound list carries the caveat to the reader.
func dispatchBudget(g *ir.Graph, env Env) []Diagnostic {
	est := estimateCost(g, env)
	var out []Diagnostic
	declared := g.Budgets != nil && g.Budgets.MaxDispatches != nil
	if declared {
		path := "spec.budgets.max_dispatches"
		d := *g.Budgets.MaxDispatches
		switch {
		case d < 0:
			out = append(out, diag(CodeWF033, path, "max_dispatches must not be negative, got %d", d))
		case env.MaxDispatches > 0 && d > env.MaxDispatches:
			out = append(out, diag(CodeWF033, path,
				"max_dispatches %d widens the host ceiling %d; the budget may narrow it but not raise it", d, env.MaxDispatches))
		}
	}
	if est.ExceedsCeiling {
		path := "spec.budgets.max_dispatches"
		if !declared {
			path = "spec.budgets"
		}
		basis := "at most"
		if !est.Complete {
			basis = "at least"
		}
		if declared {
			out = append(out, diag(CodeWF033, path,
				"worst case is %s %d dispatches, over the %d declared by budgets.max_dispatches",
				basis, est.Dispatches, est.Ceiling))
		} else {
			out = append(out, diag(CodeWF033, path,
				"worst case is %s %d dispatches, over the host ceiling %d; declare budgets.max_dispatches",
				basis, est.Dispatches, est.Ceiling))
		}
	}
	return out
}

// repetitionBounds checks WF034. Neither field is required by the decoder:
// max_iterations and max_parallel are optional pointers that resolve to nil
// untouched, so a loop without a bound reaches here rather than failing at
// WF001. This is the whole residue of that rule, and it is the one the cost
// model depends on: an unbounded repetition has no number to multiply, so
// WF033 would be estimating on air without it.
func repetitionBounds(g *ir.Graph) []Diagnostic {
	order, _ := graphNodes(g)
	var out []Diagnostic
	for _, n := range order {
		switch n.Kind {
		case ir.KindLoop:
			if n.MaxIterations == nil {
				out = append(out, diag(CodeWF034, n.Path+".max_iterations",
					"loop must declare max_iterations: without it the run has no bound to price"))
			} else if *n.MaxIterations < 1 {
				out = append(out, diag(CodeWF034, n.Path+".max_iterations",
					"loop max_iterations must be at least 1, got %d", *n.MaxIterations))
			}
		case ir.KindFanout:
			if n.MaxParallel == nil {
				out = append(out, diag(CodeWF034, n.Path+".max_parallel",
					"fan-out must declare max_parallel: without it item concurrency is unbounded"))
			} else if *n.MaxParallel < 1 {
				out = append(out, diag(CodeWF034, n.Path+".max_parallel",
					"fan-out max_parallel must be at least 1, got %d", *n.MaxParallel))
			}
		}
	}
	return out
}

// configuredCaps checks WF035: what the document asks for must fit inside
// what the host allows.
//
// Decode already caps a raw prompt at spec.MaxPromptBytes and the whole
// document at spec.MaxSpecBytes, so the prompt half of this rule is only
// the host's tighter layer above those. The vocabulary has no per-node
// output size limit, so "output limits" resolves here to the two cost
// ceilings a document can state and the host can refuse to fund: a node's
// max_turns and budgets.max_tokens. All three are one-directional for the
// same reason WF033's budget is: narrowing a host maximum is a plan the
// host can honour, widening it is a plan that will be cut off mid-run.
func configuredCaps(g *ir.Graph, env Env) []Diagnostic {
	order, _ := graphNodes(g)
	promptCap := env.MaxPromptBytes
	if promptCap <= 0 {
		promptCap = spec.MaxPromptBytes
	}
	var out []Diagnostic
	for _, n := range order {
		if n.Prompt != nil {
			out = append(out, promptFindings(n.Path+".prompt", templateText(n.Prompt), promptCap)...)
		}
		if n.Judge != nil && n.Judge.Prompt != nil {
			out = append(out, promptFindings(n.Path+".judge.prompt", templateText(n.Judge.Prompt), promptCap)...)
		}
		if env.MaxTurnsPerDispatch > 0 && n.MaxTurns != nil && *n.MaxTurns > env.MaxTurnsPerDispatch {
			out = append(out, diag(CodeWF035, n.Path+".max_turns",
				"max_turns %d exceeds the configured per-dispatch ceiling %d", *n.MaxTurns, env.MaxTurnsPerDispatch))
		}
	}
	if env.MaxTokens > 0 && g.Budgets != nil && g.Budgets.MaxTokens != nil {
		if *g.Budgets.MaxTokens > env.MaxTokens {
			out = append(out, diag(CodeWF035, "spec.budgets.max_tokens",
				"max_tokens %d widens the configured cap %d", *g.Budgets.MaxTokens, env.MaxTokens))
		}
	}
	return out
}

// promptFindings reports one prompt over the configured cap. The measure
// is the text as written: interpolated values are run state, which the
// executor bounds with its own handle thresholds.
func promptFindings(path, text string, limit int) []Diagnostic {
	if len(text) <= limit {
		return nil
	}
	return []Diagnostic{diag(CodeWF035, path,
		"prompt is %d bytes, over the configured cap %d", len(text), limit)}
}

// estimateCost walks the reachable nodes and multiplies out the
// repetitions. The reachability is WF004's, and the reason it is reused
// here is the one WF004's comment gives: a node no scheduler may enter
// spends nothing, so counting it would report a cost the run can never
// incur. The multiplier for a node is the product of its ancestors'
// repetition bounds — a delegate inside a fan-out inside a loop runs
// items times iterations — times its own retry attempts, since each
// attempt is another dispatch.
func estimateCost(g *ir.Graph, env Env) CostEstimate {
	if g == nil {
		return CostEstimate{Complete: true}
	}
	order, _ := graphNodes(g)
	reach := authorityReachable(g)
	w := &costWalk{env: env, unbound: map[string]bool{}, seen: map[*ir.Node]bool{}}
	// The walk starts at the top-level steps and reaches body nodes only
	// through their container, because the container's multiplier is what
	// prices them. graphNodes' flat order is still the frame for the
	// Unbound list: it is the one sequence guaranteed to name each node
	// exactly once.
	w.visit(g.Steps, reach, 1)

	est := CostEstimate{
		Dispatches: w.dispatches,
		Complete:   len(w.unbound) == 0,
		WallClock:  w.wallClock,
	}
	for _, n := range order {
		if w.unbound[n.Path] {
			est.Unbound = append(est.Unbound, n.Path)
		}
	}
	est.WallClockKnown = !w.wallUnknown && (env.SecondsPerDispatch > 0 || est.WallClock > 0)
	if env.MaxTurnsPerDispatch > 0 {
		est.Turns = mulCap(est.Dispatches, env.MaxTurnsPerDispatch)
		est.TurnsKnown = est.Dispatches > 0
	}
	est.Ceiling, est.ExceedsCeiling = dispatchCeiling(g, env, est)
	return est
}

// dispatchCeiling returns the bound the run must fit under and whether the
// estimate breaks it. A ceiling exists when either side declares one, and
// the tighter of the two wins; zero dispatches allowed is a real plan, so
// only the absence of both bounds leaves the axis unchecked. A negative
// declaration is ignored here rather than clamped: WF033 has already named
// it, and pricing a run against a bound that cannot mean anything would
// report the same line twice.
func dispatchCeiling(g *ir.Graph, env Env, est CostEstimate) (ceiling int, exceeds bool) {
	ceiling, set := env.MaxDispatches, env.MaxDispatches > 0
	if g.Budgets != nil && g.Budgets.MaxDispatches != nil && *g.Budgets.MaxDispatches >= 0 {
		d := *g.Budgets.MaxDispatches
		if !set || d < ceiling {
			ceiling, set = d, true
		}
	}
	if !set {
		return 0, false
	}
	return ceiling, est.Dispatches > ceiling
}

// costWalk accumulates one estimate. State is per call: like every other
// rule here, the cost model holds nothing between graphs.
type costWalk struct {
	env         Env
	dispatches  int
	wallClock   int
	wallUnknown bool
	unbound     map[string]bool
	seen        map[*ir.Node]bool
}

func (w *costWalk) visit(nodes []*ir.Node, reach map[*ir.Node]bool, factor int) {
	for _, n := range nodes {
		if n == nil || !reach[n] || w.seen[n] {
			continue // unreachable subtrees are excluded with their container
		}
		w.seen[n] = true
		e := mulCap(factor, retryAttempts(n))
		children := e

		switch n.Kind {
		case ir.KindDelegate:
			w.dispatch(n, e)
		case ir.KindGate, ir.KindSelect:
			if n.Judge != nil {
				w.dispatch(n, e)
			}
		case ir.KindCall:
			w.call(n, e)
		case ir.KindFanout:
			items, known := w.fanoutItems(n)
			if !known {
				w.unbound[n.Path] = true
			}
			children = mulCap(e, items)
		case ir.KindLoop:
			iters, known := loopIterations(n)
			if !known {
				w.unbound[n.Path] = true
			}
			children = mulCap(e, iters)
		}
		if len(n.Body) > 0 {
			w.visit(n.Body, reach, children)
		}
	}
}

// dispatch prices one node's own dispatches. A delegate dispatches once per
// execution even when WF030 has refused its role: the run would still pay
// to find out.
func (w *costWalk) dispatch(n *ir.Node, e int) {
	w.dispatches = addCap(w.dispatches, e)
	if w.wallUnknown {
		return
	}
	sec := w.env.SecondsPerDispatch
	if n.TimeoutSeconds != nil && *n.TimeoutSeconds > 0 {
		sec = *n.TimeoutSeconds
	}
	if sec <= 0 {
		w.wallUnknown = true
		w.wallClock = 0
		return
	}
	w.wallClock = addCap(w.wallClock, mulCap(e, sec))
}

// call prices a nested workflow. This graph cannot see the called document,
// so the bound is the host's configured per-call ceiling times the declared
// depth; without that ceiling the only defensible number is the depth
// itself, one dispatch per level, and the total stops being a ceiling.
func (w *costWalk) call(n *ir.Node, e int) {
	depth := 1
	if n.MaxDepth != nil && *n.MaxDepth > 1 {
		depth = *n.MaxDepth
	}
	if w.env.MaxCallDispatches > 0 {
		w.dispatches = addCap(w.dispatches, mulCap(e, mulCap(depth, w.env.MaxCallDispatches)))
		return
	}
	w.unbound[n.Path] = true
	w.dispatches = addCap(w.dispatches, mulCap(e, depth))
}

// fanoutItems bounds the number of items one fan-out visits. A literal list
// states it; a selector resolves at run time and does not, so the bound
// comes from the host ceiling when there is one and from the declared
// concurrency otherwise, which is the smallest number the document itself
// supports and is reported as incomplete.
func (w *costWalk) fanoutItems(n *ir.Node) (int, bool) {
	if n.Over != nil && n.Over.Literals != nil {
		items := len(n.Over.Literals)
		if w.env.MaxFanoutItems > 0 && items > w.env.MaxFanoutItems {
			return w.env.MaxFanoutItems, true
		}
		return items, true
	}
	if w.env.MaxFanoutItems > 0 {
		return w.env.MaxFanoutItems, true
	}
	parallel := 1
	if n.MaxParallel != nil && *n.MaxParallel > 1 {
		parallel = *n.MaxParallel
	}
	return parallel, false
}

// loopIterations bounds one loop. An absent or nonsensical bound is
// WF034's finding; the estimate still needs a number and takes the one that
// keeps the total a floor, marking the node unbound.
func loopIterations(n *ir.Node) (int, bool) {
	if n.MaxIterations == nil || *n.MaxIterations < 1 {
		return 1, false
	}
	return *n.MaxIterations, true
}

// retryAttempts counts the dispatches one node's retries may add. Absent
// or sub-one values are one attempt: the node still runs once.
func retryAttempts(n *ir.Node) int {
	if n.Retry == nil || n.Retry.MaxAttempts == nil || *n.Retry.MaxAttempts < 1 {
		return 1
	}
	return *n.Retry.MaxAttempts
}

func mulCap(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a >= costSaturate || b >= costSaturate || a > costSaturate/b {
		return costSaturate
	}
	return a * b
}

func addCap(a, b int) int {
	if a >= costSaturate || b >= costSaturate || a > costSaturate-b {
		return costSaturate
	}
	return a + b
}
