package validate

import (
	"strings"

	wfassert "github.com/buchenberg/yaah/internal/workflow/assert"
	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// CodeWF037 is declared in authority.go with the rest of the family's
// vocabulary; this file is its rule.
// over every gate in the graph, reachable or not: a
// stranded gate is still a condition nobody could evaluate, and WF004 has
// already said why it will not run.
//
// The three findings are the three ways a gate has no runnable condition:
//
//   - neither half declared, so nothing decides whether it passes;
//   - both halves declared, so the run would have two answers to choose
//     between — and which one wins is a rule this vocabulary does not have;
//   - an assert whose name or arguments the registry does not accept, stated in
//     the registry's own words so the author fixes the same thing whether they
//     read the validator or a failed run.
//
// A judge's payload is WF030's subject (its role) and the decoder's (its
// prompt is a required field), so this rule does not repeat either.
func gateConditions(g *ir.Graph) []Diagnostic {
	order, _ := graphNodes(g)
	var out []Diagnostic
	for _, n := range order {
		if n.Kind != ir.KindGate {
			continue
		}
		switch {
		case n.Assert == nil && n.Judge == nil:
			out = append(out, diag(CodeWF037, n.Path,
				"declares neither assert nor judge: a gate has to say what it checks, and this vocabulary has exactly those two conditions"))
			continue
		case n.Assert != nil && n.Judge != nil:
			out = append(out, diag(CodeWF037, n.Path+".judge",
				"declares both assert and judge: a gate has one condition, and two would leave the run choosing which answer wins"))
			continue
		}
		if n.Assert == nil {
			continue
		}
		if _, ok := wfassert.Lookup(n.Assert.Name); !ok {
			out = append(out, diag(CodeWF037, n.Path+".assert.name", "%s",
				wfassert.Problem(n.Assert.Name, n.Assert.Args)))
			continue
		}
		if why := wfassert.Problem(n.Assert.Name, n.Assert.Args); why != "" {
			path := n.Path + ".assert.args"
			if strings.Contains(why, "does not read") {
				path = n.Path + ".assert"
			}
			out = append(out, diag(CodeWF037, path, "%s", why))
		}
	}
	return out
}
