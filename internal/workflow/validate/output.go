package validate

import (
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// outputSpecs checks WF005: every declared output binds a usable name, no
// two outputs bind the same name in the scope a reader resolves it in, and
// a declared schema is well formed.
//
// The typed decoder owns everything around this already. `output` is
// either a bare name or a mapping over the known keys, the shorthand
// rejects an empty name, `type` is one of the §5.2 value types, and a
// schema that is not a string fails the decode (spec/decode.go). Two facts
// are out of reach for a per-field decoder and are the graph-level residue
// this rule owns:
//
//   - OutputSpec.Schema is a plain string: the spec package has no JSON
//     Schema value type, so nothing checks the string before here. The
//     content rules live in schema.go.
//   - output names are unique. Within one node uniqueness is structural —
//     Node.Output is a single pointer — so what is left to decide is how
//     wide the namespace is.
//
// It is one scope per container level, the same discipline §5.3 gives step
// ids and the value rules (WF010+) now re-derive for references: a name is
// unique among the top-level steps, unique among the siblings of one body,
// and a body-local name may not shadow a top-level one. Whole-graph
// uniqueness was the fail-closed interim answer, and it was wrong in a way
// authors feel immediately. A fan-out body runs once per item, so a name
// bound there is re-bound for every item and never owns one graph-wide
// handle; `steps.<id>.collect` is how anybody reads those values back. Two
// workstreams each producing `proposal` is the idiomatic shape, and under a
// global namespace the second container was refused for a collision no
// reader could actually observe. Scoping the namespace to where the name is
// resolvable keeps the guarantee that matters — a name a caller can address
// designates exactly one binding — and drops the one that cannot be
// violated.
//
// A name bound by a node with no findings still occupies its scope: the
// first binder wins and every later one is reported, so a rename fixes one
// diagnostic rather than two.
func outputSpecs(g *ir.Graph) []Diagnostic {
	order, parent := graphNodes(g)
	var out []Diagnostic

	// The top-level namespace is pre-read, both because it is the only
	// scope a body node can shadow and because a body is walked before the
	// steps that follow its container.
	topLevel := map[string]string{}
	for _, n := range g.Steps {
		if n == nil || n.Output == nil {
			continue
		}
		name := strings.TrimSpace(n.Output.Name)
		if name == "" {
			continue
		}
		if _, dup := topLevel[name]; !dup {
			topLevel[name] = n.Path
		}
	}

	bound := map[string]string{}              // top-level steps
	local := map[*ir.Node]map[string]string{} // container → its body's names
	ensure := func(container *ir.Node) map[string]string {
		if m := local[container]; m != nil {
			return m
		}
		m := map[string]string{}
		local[container] = m
		return m
	}

	for _, n := range order {
		if n == nil || n.Output == nil {
			continue
		}
		o := n.Output
		path := n.Path + ".output"
		name := strings.TrimSpace(o.Name)
		if name == "" {
			out = append(out, diag(CodeWF005, path+".name", "output name must not be empty"))
		} else if container := parent[n]; container == nil {
			if prev, dup := bound[name]; dup {
				out = append(out, diag(CodeWF005, path+".name", "output name %q is already bound at %s", o.Name, prev))
			} else {
				bound[name] = n.Path
			}
		} else if prev, shadow := topLevel[name]; shadow {
			out = append(out, diag(CodeWF005, path+".name",
				"output name %q shadows the top-level binding at %s", o.Name, prev))
		} else {
			scope := ensure(container)
			if prev, dup := scope[name]; dup {
				out = append(out, diag(CodeWF005, path+".name", "output name %q is already bound at %s", o.Name, prev))
			} else {
				scope[name] = n.Path
			}
		}
		if o.Schema == "" {
			continue
		}
		if msg := schemaProblem(o.Schema); msg != "" {
			out = append(out, diag(CodeWF005, path+".schema", "%s", msg))
		}
	}
	return out
}
