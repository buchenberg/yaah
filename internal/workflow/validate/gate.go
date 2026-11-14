package validate

import (
	"strings"

	wfassert "github.com/buchenberg/yaah/internal/workflow/assert"
	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// CodeWF037 is declared in authority.go with the rest of the family's
// vocabulary; this file is its rule.
//
// gateConditions applies this rule over every gate in the graph, reachable or
// not: a stranded gate is still a condition nobody could evaluate, and WF004
// has already said why it will not run.
//
// The four findings are the four ways a gate has no runnable condition:
//
//   - neither half declared, so nothing decides whether it passes;
//   - both halves declared, so the run would have two answers to choose
//     between — and which one wins is a rule this vocabulary does not have;
//   - an assert whose name or arguments the registry does not accept, stated in
//     the registry's own words so the author fixes the same thing whether they
//     read the validator or a failed run;
//   - a judge whose prompt is empty, so the verdict-producing dispatch has
//     nothing to ask.
//
// A judge's role is WF030's subject; its prompt is this rule's.
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
			if n.Judge.Prompt == nil {
				out = append(out, diag(CodeWF037, n.Path+".judge.prompt",
					"declares a judge with no prompt, so nobody is told what verdict to reach"))
			}
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
