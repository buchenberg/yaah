package validate

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// uniqueReferences checks WF002: node ids are unique in their scope and
// every step reference names a step that exists.
//
// ir.Resolve already enforces both halves on the authoring path — it
// rejects a duplicate top-level id, an unlabelled step, a needs entry or
// branch arm naming no step, and a body-local id that shadows a step — so
// a document that came through Validate arrives here clean and its
// defects surfaced earlier as WF001. This rule is nonetheless the graph-level
// authority, for two reasons. ir.Graph is exported and consumed by stages
// that build or edit graphs without going through Resolve (the scheduler,
// replay, tests), and validation must not assume its input was produced
// honestly. And the resolver stops at the first bad reference while this
// walk reports all of them, which is what an author with a hand-edited
// workflow file needs to see.
func uniqueReferences(g *ir.Graph) []Diagnostic {
	byID := stepsByID(g)
	order, _ := graphNodes(g)
	var out []Diagnostic

	for _, n := range g.Steps {
		if n == nil {
			continue
		}
		if n.ID == "" {
			out = append(out, diag(CodeWF002, n.Path+".id", "top-level step has no id"))
		}
	}

	// Duplicate ids among top-level steps: the first binder keeps the name
	// (stepsByID agrees) and every later one is reported at its own path.
	seen := map[string]string{}
	for _, n := range g.Steps {
		if n == nil || n.ID == "" {
			continue
		}
		if prev, dup := seen[n.ID]; dup {
			out = append(out, diag(CodeWF002, n.Path+".id", "duplicate step id %q (first bound at %s)", n.ID, prev))
			continue
		}
		seen[n.ID] = n.Path
	}

	// Step references on every node, containers included: a body node may
	// need a top-level step, so needs is checked over the whole walk. Only
	// branch and gate nodes populate the target fields, so no kind test is
	// needed; whether a payload field belongs to its kind is a later rule's
	// question.
	for _, n := range order {
		for i, ref := range n.Needs {
			if _, ok := byID[ref]; !ok {
				out = append(out, diag(CodeWF002, fmt.Sprintf("%s.needs[%d]", n.Path, i), "unknown step id %q", ref))
			}
		}
		for i, c := range n.Cases {
			if _, ok := byID[c.Then]; !ok {
				out = append(out, diag(CodeWF002, fmt.Sprintf("%s.cases[%d].then", n.Path, i), "unknown step id %q", c.Then))
			}
		}
		if n.Default != "" {
			if _, ok := byID[n.Default]; !ok {
				out = append(out, diag(CodeWF002, n.Path+".default", "unknown step id %q", n.Default))
			}
		}
		if n.OnPass != "" {
			if _, ok := byID[n.OnPass]; !ok {
				out = append(out, diag(CodeWF002, n.Path+".on_pass", "unknown step id %q", n.OnPass))
			}
		}
		if n.OnFail != "" {
			if _, ok := byID[n.OnFail]; !ok {
				out = append(out, diag(CodeWF002, n.Path+".on_fail", "unknown step id %q", n.OnFail))
			}
		}
	}

	// Body-local ids: positional siblings must not collide with each other
	// and must not shadow a top-level step, or the lexical scope that
	// templates and selectors resolve against has two meanings for one
	// name (§5.3). An empty body id is legal and expected.
	for _, container := range order {
		if len(container.Body) == 0 {
			continue
		}
		local := map[string]string{}
		for _, b := range container.Body {
			if b == nil || b.ID == "" {
				continue
			}
			path := b.Path + ".id"
			if _, top := byID[b.ID]; top {
				out = append(out, diag(CodeWF002, path, "body-local id %q shadows a top-level step id", b.ID))
				continue
			}
			if prev, dup := local[b.ID]; dup {
				out = append(out, diag(CodeWF002, path, "duplicate body-local id %q (first bound at %s)", b.ID, prev))
				continue
			}
			local[b.ID] = b.Path
		}
	}
	return out
}
