package validate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// typesCompatible checks WF011: a value a consumer reads must be the kind of
// value that consumer acts on.
//
// The check is shallow by design and the vocabulary is the one §5.2 declares:
// the six named types (Report, Proposal, Settled, Verdict, Plan, Artifact),
// the shapes an inline JSON Schema commits to (array, object, scalar), and
// the parameter types a workflow declares. Anything a graph leaves undeclared
// is unknown and no finding, which is what keeps the golden corpus clean: a
// step that binds `output: Plan` binds a name and no shape, so
// `steps.plan.output.workstreams` is a path nobody can type here.
//
// What a shape must be definite about is the consumer's need, which is where
// the matrix comes from:
//
//	fanout.over       must read a list      a fan-out binds one item per element
//	settle.proposals  must read a list      a settle walks a set, and its element
//	                                       type must be Proposal when declared
//	settle.where      must read a list      where is a filter over a set
//	select.candidates must read a list      a policy picks among a set
//	branch.on         must not read a list  one value is matched against the arms
//	aggregate.inputs  must read an object   strategy json_merge merges mappings
//	seed_from, continue_from, fork_from
//	                  must not read a list  each names one prior run
//	producing node    type and schema agree an output cannot be a Proposal and a
//	                                       list at once
//
// Every row defers to `as_list: true`, which is the author's explicit
// statement that a single value is being wrapped — the case the plan lists as
// legal, and the shape speculative.golden's land_one settles.
func typesCompatible(c *valueCtx) []Diagnostic {
	var out []Diagnostic
	for _, n := range c.order {
		if n == nil {
			continue
		}
		switch n.Kind {
		case ir.KindFanout:
			if n.Over != nil && n.Over.Selector != nil {
				out = append(out, requireList(c, valueSite(n, "over"), n.Over.Selector,
					"fanout.over", "a fan-out")...)
			}
		case ir.KindSettle:
			out = append(out, requireList(c, valueSite(n, "proposals"), n.Proposals,
				"settle.proposals", "a settle")...)
			out = append(out, settleElementsAreProposals(c, n)...)
			out = append(out, requireList(c, valueSite(n, "where"), n.Where,
				"settle.where", "a where filter")...)
		case ir.KindSelect:
			out = append(out, requireList(c, valueSite(n, "candidates"), n.Candidates,
				"select.candidates", "a selection policy")...)
		case ir.KindBranch:
			out = append(out, requireNotList(c, valueSite(n, "on"), n.On,
				"branch.on", "a branch")...)
		case ir.KindAggregate:
			out = append(out, aggregateInputsMergeable(c, n)...)
		case ir.KindDelegate, ir.KindLoop:
			out = append(out, singleValueBindings(c, n)...)
		}
		out = append(out, outputTypeAgreesWithSchema(n)...)
	}
	return out
}

func requireList(c *valueCtx, at site, sel *ir.Selector, label, consumer string) []Diagnostic {
	if sel == nil || sel.From == nil {
		return nil
	}
	sh, _ := c.shapeAt(at.node, sel.From)
	if sel.AsList || !sh.notList() {
		return nil
	}
	return []Diagnostic{diag(CodeWF011, at.path+".from",
		"%s reads %s; %s requires a list; name a field that is a list or wrap the value with as_list: true",
		label, sh.label(), consumer)}
}

func requireNotList(c *valueCtx, at site, sel *ir.Selector, label, consumer string) []Diagnostic {
	if sel == nil || sel.From == nil {
		return nil
	}
	sh, _ := c.shapeAt(at.node, sel.From)
	if sh.kind != kindList {
		return nil
	}
	return []Diagnostic{diag(CodeWF011, at.path+".from",
		"%s reads %s; %s needs one value to match, not a list", label, sh.label(), consumer)}
}

// settleElementsAreProposals is the one element-type check this family makes,
// because settle is the one consumer whose verbs — select, apply, release,
// discard — are defined over proposals and nothing else. It fires only on a
// declared contradiction: an author who typed the producer as a Verdict is
// describing a value a settle cannot act on.
func settleElementsAreProposals(c *valueCtx, n *ir.Node) []Diagnostic {
	sel := n.Proposals
	if sel == nil || sel.From == nil {
		return nil
	}
	sh, _ := c.shapeAt(n, sel.From)
	elem := sh.element()
	if elem.kind != kindRecord || elem.name == "" || elem.name == string(ir.OutputProposal) {
		return nil
	}
	return []Diagnostic{diag(CodeWF011, valueSite(n, "proposals").path+".from",
		"settle.proposals reads %s and a settle acts on Proposal values", elem.label())}
}

// aggregateInputsMergeable checks the one strategy whose name commits to a
// shape. concat and summarize coerce, so only a producer that cannot answer
// the question is reported.
func aggregateInputsMergeable(c *valueCtx, n *ir.Node) []Diagnostic {
	if n.Strategy != ir.AggregateJSONMerge {
		return nil
	}
	var out []Diagnostic
	for i := range n.Inputs {
		sel := n.Inputs[i]
		if sel == nil || sel.From == nil {
			continue
		}
		at := valueSite(n, fmt.Sprintf("inputs[%d]", i))
		sh, _ := c.shapeAt(n, sel.From)
		switch sh.kind {
		case kindObject, kindRecord:
			continue
		case kindList:
			if sh.elem == nil || sh.elem.kind == kindObject || sh.elem.kind == kindRecord {
				continue
			}
		case kindUnknown:
			continue
		}
		out = append(out, diag(CodeWF011, at.path+".from",
			"aggregate.inputs reads %s and strategy json_merge merges objects", sh.label()))
	}
	return out
}

// singleValueBindings covers the delegate and loop fields that name one
// earlier run. They are templates, so their values are rendered to text and
// no type is demanded of them; the question is only whether the reference can
// mean one thing at all.
func singleValueBindings(c *valueCtx, n *ir.Node) []Diagnostic {
	var out []Diagnostic
	for _, f := range []struct {
		key string
		t   *ir.Template
	}{{"seed_from", n.SeedFrom}, {"continue_from", n.ContinueFrom}, {"fork_from", n.ForkFrom}} {
		if f.t == nil {
			continue
		}
		at := valueSite(n, f.key)
		for _, r := range f.t.Refs {
			src := sourceOfRef(r)
			if src == nil {
				continue
			}
			sh, _ := c.shapeAt(n, src)
			if sh.kind != kindList {
				continue
			}
			out = append(out, diag(CodeWF011, at.path+"["+r.String()+"]",
				"%s must name one prior run and reads %s", f.key, sh.label()))
		}
	}
	return out
}

// outputTypeAgreesWithSchema checks a producer against itself. A §5.2 value
// type is a record, so an output that declares both `type: Report` and a
// schema describing a list or a scalar describes two different values, and one
// of the two is a lie to the next consumer.
func outputTypeAgreesWithSchema(n *ir.Node) []Diagnostic {
	if n.Output == nil || n.Output.Type == "" {
		return nil
	}
	sch := inlineSchema(n.Output.Schema)
	if sch == nil {
		return nil
	}
	if sh := shapeOfSchema(sch); sh.kind == kindList || sh.kind == kindScalar {
		return []Diagnostic{diag(CodeWF011, n.Path+".output.type",
			"output type %q is a record but the declared schema describes %s",
			string(n.Output.Type), sh.label())}
	}
	return nil
}

// declaredFieldList spells the fields an object schema declares, for the
// message an author acts on. It is bounded so a wide schema cannot bury the
// finding.
func declaredFieldList(sch map[string]any) string {
	props, _ := sch["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	if len(names) == 0 {
		return "no fields"
	}
	sort.Strings(names)
	if len(names) > 6 {
		return strings.Join(names[:6], ", ") + fmt.Sprintf(" (+%d more)", len(names)-6)
	}
	return strings.Join(names, ", ")
}
