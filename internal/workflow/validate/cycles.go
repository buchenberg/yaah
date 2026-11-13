package validate

import (
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// MaxCallDepth is the ceiling on a call node's declared recursion depth.
// The bound lives in the validator rather than only in the runtime so a
// runaway workflow is reported from the document, before a run spends its
// dispatch budget discovering the same thing at depth 17.
const MaxCallDepth = 16

// needsCycles checks the acyclic half of WF003 over the top-level needs
// graph. Body nodes are excluded deliberately: a needs entry may name only
// a top-level step, so every cycle runs between steps and a body node can
// neither form one nor be a member of one.
//
// Each cycle is reported once, at the first member in document order, with
// the shortest chain back to that member rendered as an arrow sequence —
// "needs cycle: a -> b -> a" — because the useful message names the loop an
// author has to break, not the whole component. Arrows follow needs: a -> b
// reads "a needs b".
func needsCycles(g *ir.Graph) []Diagnostic {
	byID := stepsByID(g)
	var ids []string
	edges := map[string][]string{}
	for _, n := range g.Steps {
		if n == nil || n.ID == "" {
			continue
		}
		if _, dup := edges[n.ID]; dup {
			continue // duplicate id: reported by WF002, and unindexable here
		}
		ids = append(ids, n.ID)
		var deps []string
		for _, ref := range n.Needs {
			// An unresolvable reference is WF002's finding and is not an
			// edge: it cannot close a cycle.
			if _, ok := byID[ref]; ok {
				deps = append(deps, ref)
			}
		}
		edges[n.ID] = deps
	}

	var out []Diagnostic
	for _, comp := range cyclicComponents(ids, edges) {
		start := firstIn(ids, comp)
		members := make(map[string]bool, len(comp))
		for _, id := range comp {
			members[id] = true
		}
		chain := cycleChain(start, edges, members)
		out = append(out, diag(CodeWF003, byID[start].Path+".needs", "needs cycle: %s", strings.Join(chain, " -> ")))
	}
	return out
}

// callDepth checks the bounded-depth half of WF003.
//
// ir.KindCall and its payload (workflow, params, max_depth) already exist
// in the vocabulary, so the declared bound is checkable now: max_depth must
// be a positive number no larger than MaxCallDepth, and a call that names
// its own workflow must declare one, since without it the recursion is
// unbounded by construction.
//
// What one resolved graph cannot see is the cycle that crosses documents: a
// calls b, b calls a. Detecting that needs the registry that resolves
// Node.Workflow to another ir.Graph, and the check lands with that bead.
// The self-call test below is name equality against Graph.Name — the
// metadata.name that ir.Resolve carries verbatim — so a call reaching the
// same document under an alias or a file path slips past it: a heuristic,
// not a guarantee. Until the registry lands the runtime depth guard is the
// only bound on mutual recursion, and it must not be dropped.
func callDepth(g *ir.Graph) []Diagnostic {
	order, _ := graphNodes(g)
	var out []Diagnostic
	for _, n := range order {
		if n == nil || n.Kind != ir.KindCall {
			continue
		}
		path := n.Path + ".max_depth"
		d := n.MaxDepth
		switch {
		case d != nil && *d < 1:
			out = append(out, diag(CodeWF003, path, "call max_depth must be at least 1, got %d", *d))
		case d != nil && *d > MaxCallDepth:
			out = append(out, diag(CodeWF003, path, "call max_depth %d exceeds the limit %d", *d, MaxCallDepth))
		case d == nil && n.Workflow != "" && n.Workflow == g.Name:
			out = append(out, diag(CodeWF003, path, "call names its own workflow %q and must declare max_depth", g.Name))
		}
	}
	return out
}

// firstIn returns the id from comp that comes earliest in the document
// order slice.
func firstIn(order []string, comp []string) string {
	for _, id := range order {
		for _, member := range comp {
			if id == member {
				return id
			}
		}
	}
	return comp[0]
}

// cycleChain walks needs edges from start back to start, staying inside the
// component, and returns the chain with start repeated at the end: ["a","b","a"].
// Breadth-first, so the chain is the shortest one and the message does not
// wander the component on its way home. Every member of a cyclic component
// has such a path; the fallback names the loop as a bare self-edge rather
// than returning nothing.
func cycleChain(start string, edges map[string][]string, in map[string]bool) []string {
	parent := map[string]string{start: ""}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range edges[cur] {
			if !in[dep] {
				continue
			}
			if dep == start {
				var middle []string
				for n := cur; n != start; n = parent[n] {
					middle = append(middle, n)
				}
				chain := []string{start}
				for i := len(middle) - 1; i >= 0; i-- {
					chain = append(chain, middle[i])
				}
				return append(chain, start)
			}
			if _, seen := parent[dep]; seen {
				continue
			}
			parent[dep] = cur
			queue = append(queue, dep)
		}
	}
	return []string{start, start}
}

// cyclicComponents returns the strongly connected components of the needs
// graph that participate in a cycle: a component of more than one id, or a
// single id that needs itself. Iteration follows the document order of ids
// and the needs order within each node, so both the components and their
// order are fixed for a given graph.
func cyclicComponents(ids []string, edges map[string][]string) [][]string {
	t := &tarjan{
		edges:   edges,
		index:   map[string]int{},
		low:     map[string]int{},
		onStack: map[string]bool{},
	}
	for _, id := range ids {
		if _, seen := t.index[id]; !seen {
			t.visit(id)
		}
	}
	return t.out
}

// tarjan is Tarjan's algorithm over the needs edges. Graph depth is capped
// by spec.MaxNodes, so the recursion is bounded by construction.
type tarjan struct {
	edges   map[string][]string
	index   map[string]int
	low     map[string]int
	onStack map[string]bool
	stack   []string
	next    int
	out     [][]string
}

func (t *tarjan) visit(id string) {
	t.index[id] = t.next
	t.low[id] = t.next
	t.next++
	t.stack = append(t.stack, id)
	t.onStack[id] = true

	for _, dep := range t.edges[id] {
		if _, seen := t.index[dep]; !seen {
			t.visit(dep)
			if t.low[dep] < t.low[id] {
				t.low[id] = t.low[dep]
			}
			continue
		}
		if t.onStack[dep] && t.index[dep] < t.low[id] {
			t.low[id] = t.index[dep]
		}
	}

	if t.low[id] != t.index[id] {
		return
	}
	var comp []string
	for {
		last := t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
		t.onStack[last] = false
		comp = append(comp, last)
		if last == id {
			break
		}
	}
	if len(comp) > 1 || needsSelf(t.edges, id) {
		t.out = append(t.out, comp)
	}
}

func needsSelf(edges map[string][]string, id string) bool {
	for _, dep := range edges[id] {
		if dep == id {
			return true
		}
	}
	return false
}
