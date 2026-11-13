package validate

import "github.com/buchenberg/yaah/internal/workflow/ir"

// unreachable checks WF004: every node must be reachable from the entry
// set, which is the top-level steps that declare no needs — the nodes a
// scheduler may start with. A body node is entered through its container,
// so it is reachable when its container is.
//
// Reachability follows the executor's join-all needs: a node that declares
// needs is enterable only once every step it needs is itself enterable,
// because the scheduler waits for all of them before entering it. One need
// that never completes therefore strands the node as surely as an island
// does. Two things break the chain, and both are already named by another
// rule: a needs cycle (WF003 reports the loop) and a reference no node
// satisfies (WF002). The overlap is deliberate. WF002 and WF003 are the
// document defects an author fixes; WF004 is the scheduling consequence
// that makes a run refuse to start, and it names every step stranded behind
// the defect rather than only the defect itself. A graph whose needs is
// acyclic and fully bound therefore always satisfies WF004 by construction
// — that is the theorem this rule checks, not a heuristic.
func unreachable(g *ir.Graph) []Diagnostic {
	order, parent := graphNodes(g)
	byID := stepsByID(g)
	reach := make(map[*ir.Node]bool, len(order))

	// Least fixed point: mark what is provably enterable until a round
	// marks nothing. Monotone, so at most len(order) rounds; spec.MaxNodes
	// bounds the work absolutely.
	for {
		progressed := false
		for _, n := range order {
			if reach[n] {
				continue
			}
			if up := parent[n]; up != nil && !reach[up] {
				continue // entered only through a container not entered yet
			}
			enterable := true // no needs: an entry step (vacuous join-all)
			for _, ref := range n.Needs {
				if dep, ok := byID[ref]; !ok || !reach[dep] {
					enterable = false
					break
				}
			}
			if enterable {
				reach[n] = true
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}

	var out []Diagnostic
	for _, n := range order {
		if reach[n] {
			continue
		}
		if up := parent[n]; up != nil && !reach[up] {
			out = append(out, diag(CodeWF004, n.Path, "body node is unreachable because its container %s is", up.Path))
			continue
		}
		// A node here always has at least one blocking need: with none it
		// is an entry step and was marked above, and an unreachable
		// container took the previous branch. Name the first blocker in
		// needs order so the message is deterministic and points at a
		// root the author can fix.
		for _, ref := range n.Needs {
			if dep, ok := byID[ref]; !ok {
				out = append(out, diag(CodeWF004, n.Path, "not reachable from the entry set: need %q names no step", ref))
				break
			} else if !reach[dep] {
				out = append(out, diag(CodeWF004, n.Path, "not reachable from the entry set: need %q is not reachable", ref))
				break
			}
		}
	}
	return out
}
