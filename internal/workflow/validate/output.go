package validate

import (
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// outputSpecs checks WF005: every declared output binds a usable name, no
// two outputs bind the same name, and a declared schema is well formed.
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
//     Node.Output is a single pointer — so the only scope in which a
//     duplicate is even expressible, and the scope the value rules (WF010+)
//     bind names against, is the whole graph. Two nodes claiming one name
//     make a name-shaped handle ambiguous even though the path
//     `steps.<id>.output` is not.
//
// A name bound by a node with no findings still occupies the namespace:
// the first binder wins and every later one is reported, so a rename fixes
// one diagnostic rather than two.
func outputSpecs(g *ir.Graph) []Diagnostic {
	order, _ := graphNodes(g)
	var out []Diagnostic
	bound := map[string]string{}

	for _, n := range order {
		if n == nil || n.Output == nil {
			continue
		}
		o := n.Output
		path := n.Path + ".output"
		if strings.TrimSpace(o.Name) == "" {
			out = append(out, diag(CodeWF005, path+".name", "output name must not be empty"))
		} else if prev, dup := bound[o.Name]; dup {
			out = append(out, diag(CodeWF005, path+".name", "output name %q is already bound at %s", o.Name, prev))
		} else {
			bound[o.Name] = n.Path
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
