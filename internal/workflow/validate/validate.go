package validate

import (
	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// Structural runs the structural family (WF002–WF005) over a resolved
// graph and returns every finding. Rules run in code order and each rule
// reports in document order, so the slice is deterministic for a given
// graph: it can be compared, counted, or shown verbatim without sorting
// first. A nil graph has nothing to check and yields no findings.
//
// Later families (values and templates, proposal linearity, authority and
// cost) are separate aggregates over the same graph. They do not join here
// — "structural" is the name of this family, not a stage in a pipeline.
func Structural(g *ir.Graph) []Diagnostic {
	if g == nil {
		return nil
	}
	var out []Diagnostic
	out = append(out, uniqueReferences(g)...)
	out = append(out, needsCycles(g)...)
	out = append(out, callDepth(g)...)
	out = append(out, unreachable(g)...)
	out = append(out, outputSpecs(g)...)
	return out
}

// Validate is the end-to-end seam: workflow document bytes in, the
// resolved graph and its diagnostics out. A document that strict decoding
// or resolution rejects never becomes a graph, so the failure is reported
// as one WF001 diagnostic through Diagnose; otherwise Structural's
// findings are returned.
//
// Return contract: a document that parses and resolves yields a non-nil
// graph and diagnostics carrying only WF002–WF005 findings (possibly
// none); a document that never became a graph yields a nil graph and
// exactly one WF001. The graph is therefore nil exactly when the
// diagnostics contain a WF001. Callers that want to run a validated
// workflow should require zero diagnostics, which is what the spec
// goldens assert.
func Validate(data []byte) (*ir.Graph, []Diagnostic) {
	doc, err := spec.Parse(data)
	if err != nil {
		return nil, Diagnose(err)
	}
	g, err := ir.Resolve(doc)
	if err != nil {
		return nil, Diagnose(err)
	}
	return g, Structural(g)
}
