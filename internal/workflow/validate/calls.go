package validate

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// CallDocument is one resolvable subworkflow, and CallResolver is the seam the
// CLI's registry hangs from: validation judges name resolution, cycles, and
// depth over the graphs this answers with, and never reads a file itself.
type (
	CallDocument struct {
		Name     string
		SpecHash string
		Graph    *ir.Graph
	}
	CallResolver interface {
		ResolveCall(name string) (CallDocument, bool)
	}
)

// analysisTraversalCap bounds the cross-document walk. Depth alone caps a
// well-formed DAG, but a hostile registry (many documents, many edges) should
// spend a bounded budget before the walk reports a conservative refusal
// instead of burning the analysis. 2^16 traversals is far past any real spec
// and cheap to reach for a hostile one.
const analysisTraversalCap = 1 << 16

// Calls runs the call family over a resolved graph: the reference half of
// WF002 for a call node's `workflow`, and the cross-document half of WF003 —
// the cycles and the total depth the plan's single-document rules cannot see.
// The family is separate from Structural because it needs the registry the
// CLI resolves names against: without one, every call is reported as
// unresolvable, which is fail-closed by design and the message says why.
//
// What it reports:
//
//   - a call naming no workflow, or a name that is not a subworkflow identifier
//     (path shapes — "../secrets", "sub/../../x" — are refused as names, which
//     is the containment rule's first half; the second half lives in the
//     registry's path check);
//   - a call the registry cannot resolve — no registry supplied, or no
//     document under --workflow-root carries that name;
//   - a cycle across documents (a -> b -> a) — the direct self-call stays the
//     one legal recursion, bounded by its declared max_depth the same way
//     callDepth bounds it in a single document;
//   - a call chain whose total depth exceeds MaxCallDepth, counting each
//     re-entry a self-call's bound allows.
//
// Determinism: the walk follows the root's call nodes in canonical order and
// each document's in turn, reports each finding the first time its edge is
// seen, and a finding per violating node, so two runs over one registry agree.
func Calls(g *ir.Graph, env Env) []Diagnostic {
	if g == nil {
		return nil
	}
	var out []Diagnostic
	order, _ := graphNodes(g)
	reach := authorityReachable(g)
	for _, n := range order {
		if n == nil || n.Kind != ir.KindCall || !reach[n] {
			continue
		}
		switch {
		case n.Workflow == "":
			out = append(out, diag(CodeWF002, n.Path+".workflow",
				"call names no workflow: the field is required and a call without it may not run"))
		case !spec.ValidSubworkflowName(n.Workflow):
			out = append(out, diag(CodeWF002, n.Path+".workflow",
				"call names %q, which is not a subworkflow name: names are letters, digits, underscore, and hyphen, resolved under --workflow-root — file paths are refused", n.Workflow))
		case env.Calls == nil:
			out = append(out, diag(CodeWF002, n.Path+".workflow",
				"call names %q but no subworkflow registry is configured: pass --workflow-root <dir> to resolve calls", n.Workflow))
		default:
			if _, ok := env.Calls.ResolveCall(n.Workflow); !ok {
				out = append(out, diag(CodeWF002, n.Path+".workflow",
					"call names %q, which the registry resolves to no workflow: no %s under --workflow-root is a loadable spec named that", n.Workflow, subworkflowFileHint(n.Workflow)))
			}
		}
	}
	out = append(out, callGraphBounds(g, env)...)
	return out
}

func subworkflowFileHint(name string) string {
	return strings.Join([]string{name + ".yaml", name + ".json"}, " or ")
}

// callGraphBounds is the cross-document half of WF003: cycles and the total
// depth of the call graph reachable from g.
//
// The walk carries a path of document names and, for each call node on the
// path, the re-entries its declared max_depth still allows. Entering any
// document costs one level of depth; re-entering a document already on the
// path is a cycle and refused unless the file names the same document it is
// calling from — a direct self-call — and its declared bound admits the
// re-entry. Depth beyond MaxCallDepth is the "cost bomb no document value
// bounds" case, and the finding names the chain so the author sees which
// calls compose into it.
func callGraphBounds(g *ir.Graph, env Env) []Diagnostic {
	if env.Calls == nil {
		return nil
	}
	w := &callWalk{
		env:    env,
		stack:  []string{docName(g, "entry")},
		unwind: map[string]bool{},
		seen:   map[*ir.Node]bool{},
		selves: map[*ir.Node]int{},
		limit:  analysisTraversalCap,
	}
	if w.stack[0] == "" {
		return nil
	}
	return w.bounds(g)
}

// docName labels a document in a chain message. The registry keys names, but
// the root graph is only a name when its metadata says so; an unnamed entry
// is the chain's head and prints as "entry" so every chain message is a readable
// sequence of identifiers.
func docName(g *ir.Graph, fallback string) string {
	if g != nil && g.Name != "" {
		return g.Name
	}
	return fallback
}

type callWalk struct {
	env    Env
	stack  []string
	unwind map[string]bool   // cycle member sets already reported
	seen   map[*ir.Node]bool // depth violations already reported per edge node
	selves map[*ir.Node]int  // remaining self re-entries per call node on the path
	limit  int
}

func (w *callWalk) bounds(g *ir.Graph) []Diagnostic {
	var out []Diagnostic
	order, _ := graphNodes(g)
	reach := authorityReachable(g)
	for _, n := range order {
		if n == nil || n.Kind != ir.KindCall || !reach[n] || n.Workflow == "" {
			continue
		}
		out = append(out, w.edge(g, n)...)
	}
	return out
}

// edge follows one call node's edge from the document it lives in.
func (w *callWalk) edge(from *ir.Graph, n *ir.Node) []Diagnostic {
	doc, ok := w.env.Calls.ResolveCall(n.Workflow)
	if !ok || doc.Graph == nil {
		return nil // the family's reference half already reported the name
	}
	w.limit--
	if w.limit < 0 {
		return []Diagnostic{diag(CodeWF003, n.Path+".workflow",
			"call graph exceeds the analysis traversal budget %d: the registry composes more call edges than any spec plausibly holds", analysisTraversalCap)}
	}
	self := n.Workflow == w.stack[len(w.stack)-1]
	if idx := indexOf(w.stack, n.Workflow); idx >= 0 && !self {
		// A back edge to another document on the path: the cycle crosses
		// documents, and no declared bound makes it analyzable — refuse it.
		chain := strings.Join(append(append([]string(nil), w.stack[idx:]...), n.Workflow), " -> ")
		if !w.unwind[chain] {
			w.unwind[chain] = true
			return []Diagnostic{diag(CodeWF003, n.Path+".workflow",
				"call cycle across documents: %s — recursion between distinct documents is refused; inline the work or bind it with a direct self-call's max_depth", chain)}
		}
		return nil
	}
	if len(w.stack)+1 > MaxCallDepth {
		if !w.seen[n] {
			w.seen[n] = true
			chain := strings.Join(append(append([]string(nil), w.stack...), n.Workflow), " -> ")
			return []Diagnostic{diag(CodeWF003, n.Path+".workflow",
				"call chain depth %d exceeds the limit %d: %s", len(w.stack)+1, MaxCallDepth, chain)}
		}
		return nil
	}
	if self {
		// The one legal recursion: re-entering the document its own call node
		// names. The declared bound is the number of times this node's edge
		// may be taken along one path; callDepth already refused the
		// undeclared and over-ceiling forms, so a missing bound here prices
		// as one and lets the depth ceiling catch the rest.
		if _, seen := w.selves[n]; !seen {
			w.selves[n] = selfBudget(n) - 1
		} else if w.selves[n] <= 0 {
			return nil
		} else {
			w.selves[n]--
		}
	}
	w.stack = append(w.stack, n.Workflow)
	out := w.bounds(doc.Graph)
	w.stack = w.stack[:len(w.stack)-1]
	return out
}

func indexOf(xs []string, v string) int {
	for i, x := range xs {
		if x == v {
			return i
		}
	}
	return -1
}

// selfBudget is the number of times one call node's edge may appear on a
// single path: its declared max_depth, or 1 when undeclared. callDepth
// refuses the values below 1 and above MaxCallDepth, so this is a floor.
func selfBudget(n *ir.Node) int {
	if n == nil || n.MaxDepth == nil || *n.MaxDepth < 1 {
		return 1
	}
	return *n.MaxDepth
}

// selfFactor is the number of times a document's own passes run along its
// deepest recursion path: the pass this walk prices, plus one traversal of the
// declared bound for each self-call inside it (each traversal enters another
// full copy of the document). It prices the repetition the cost model charges
// for a self-recursive workflow, and it is the same number the runtime's
// per-workflow guard enforces: a copy of the workflow per traversal, the
// outer pass included, is what max_depth: b means everywhere this package
// and the door read it.
func selfFactor(document string, g *ir.Graph) int {
	if g == nil {
		return 1
	}
	order, _ := graphNodes(g)
	factor := 1
	for _, n := range order {
		if !selfCall(document, n) {
			continue
		}
		factor += selfBudget(n)
	}
	return factor
}

// selfCall reports whether n calls the document it lives in — "itself" as
// the entry points know it: the registry name the document was loaded under.
// The walk's path identity is the registry name, not metadata.name: a helper
// that calls its own name is the one legal recursion however it spells its
// metadata, and a document calling another entry that happens to share its
// metadata name is a cross-document edge the walk follows normally.
func selfCall(document string, n *ir.Node) bool {
	return document != "" && n != nil && n.Kind == ir.KindCall && n.Workflow == document
}

// callCostMemo prices registry-resolved call targets across the cost walk.
// Entries are per workflow name: a document's one-pass cost is computed once
// and every caller reuses it, which is what keeps the estimate's arithmetic
// linear in edges even with a diamond-shaped call graph.
type callCostMemo struct {
	env  Env
	memo map[string]callCostEntry
}

func newCallCostMemo(env Env) *callCostMemo {
	return &callCostMemo{env: env, memo: map[string]callCostEntry{}}
}

type callCostEntry struct {
	// state: 1 = miss (unresolvable here, or currently being computed: a
	// cycle prices its back edge as a floor the same way), 2 = computed
	state      int
	dispatches int
	wallClock  int
	wallKnown  bool
	unbound    map[string]bool
}

// charge returns the worst-case cost of one execution of the named workflow,
// recursing through the registry. The child's own call nodes are priced the
// same way except for self-calls, whose repetition the document's selfFactor
// carries: charging them again would count the recursion squared.
func (m *callCostMemo) charge(name string) callCostEntry {
	if e, ok := m.memo[name]; ok {
		return e
	}
	// Mark a miss until proven otherwise: a cycle in the charge path resolves
	// to the memo's in-progress entry only through this pre-registration, and
	// callDepth's family refuses those cycles before a run prices them.
	m.memo[name] = callCostEntry{state: 1}
	doc, ok := m.env.Calls.ResolveCall(name)
	if !ok || doc.Graph == nil {
		return m.memo[name]
	}
	reach := authorityReachable(doc.Graph)
	w := &costWalk{
		env:      m.env,
		unbound:  map[string]bool{},
		seen:     map[*ir.Node]bool{},
		calls:    m,
		selfName: name,
	}
	w.visit(doc.Graph.Steps, reach, 1)
	unbound := map[string]bool{}
	for k := range w.unbound {
		unbound[fmt.Sprintf("%s/%s", name, k)] = true
	}
	e := callCostEntry{
		state:      2,
		dispatches: mulCap(w.dispatches, selfFactor(name, doc.Graph)),
		wallClock:  mulCap(w.wallClock, selfFactor(name, doc.Graph)),
		wallKnown:  !w.wallUnknown,
		unbound:    unbound,
	}
	m.memo[name] = e
	return e
}
