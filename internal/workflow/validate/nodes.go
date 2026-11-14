package validate

import "github.com/buchenberg/yaah/internal/workflow/ir"

// graphNodes returns every node in the graph in document order — each
// top-level step followed, depth-first, by the nodes of its body — and a
// map from a node to the container that holds it (nil for top-level
// steps). Both are computed per call: a validator holds no state across
// graphs and never caches a caller's pointers.
//
// The seen guard is not paranoia about ir.Resolve, which cannot alias one
// node into two containers or make a body contain itself. ir.Graph is an
// exported type with fields a scheduler, a replay, or a test can edit
// directly, and a structural walk that hung or recursed forever on such a
// graph would be a denial of service in the CLI. A revisited node is
// simply not descended into again.
func graphNodes(g *ir.Graph) (order []*ir.Node, parent map[*ir.Node]*ir.Node) {
	parent = map[*ir.Node]*ir.Node{}
	if g == nil {
		return nil, parent
	}
	seen := map[*ir.Node]bool{}
	var walk func(nodes []*ir.Node, up *ir.Node)
	walk = func(nodes []*ir.Node, up *ir.Node) {
		for _, n := range nodes {
			if n == nil || seen[n] {
				continue
			}
			seen[n] = true
			order = append(order, n)
			parent[n] = up
			walk(n.Body, n)
		}
	}
	walk(g.Steps, nil)
	return order, parent
}

// stepsByID indexes the top-level steps by id. Needs entries, branch arms,
// gate dispositions, and an assert's `from` subject may name a top-level step
// and nothing else, so this map is the whole reference namespace for the graph
// rules. On a duplicate id the first binder wins, because the duplicate itself
// is uniqueReferences' finding and must not cascade into every reference to
// it.
func stepsByID(g *ir.Graph) map[string]*ir.Node {
	byID := map[string]*ir.Node{}
	if g == nil {
		return byID
	}
	for _, n := range g.Steps {
		if n == nil || n.ID == "" {
			continue
		}
		if _, dup := byID[n.ID]; !dup {
			byID[n.ID] = n
		}
	}
	return byID
}
