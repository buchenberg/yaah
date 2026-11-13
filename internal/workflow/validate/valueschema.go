package validate

import (
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// fieldsDeclared checks WF012: a field a value reads exists in the shape the
// producer declares.
//
// Three forms of the same mistake are reported here, and all three stay
// silent unless somebody declared a shape:
//
//   - a template or selector access path, `steps.plan.output.workstreams`,
//     whose segment is not in the output's inline schema. A named schema
//     (`schema: report.v1`) points at a registry this package does not hold,
//     so it declares nothing here and nothing is decided; that is the whole
//     reason the golden corpus, which binds `output: Plan` and no schema,
//     produces no findings.
//   - a field read off a value that cannot have fields: a list binds its
//     fields per item through `field:`, and a scalar has none, so naming a
//     field in the path is a shape error even before the run starts.
//   - a selector's `field:` — the property a settle filter, a loop
//     terminator, or a fan-out projection reads per item — checked against
//     the element schema of the list it reads.
//
// The last form is the one place in the package that reads the authoring
// document instead of the graph, and it does so under protest:
// ir.compileSelector builds the compiled Selector from `from`, the predicate,
// and `as_list` and never carries Selector.Field forward (bead yaah-0u0v.32),
// so the field an author declared is invisible in the IR. Validating it from
// the spec here is what lets the typo be caught at all; the counterpart
// defect — that the runtime will ignore a `field:` even when this rule calls
// it well-formed — is not this rule's to fix, and is reported as such. Once
// .32 lands, this check should switch to the compiled form and lose its doc
// parameter.
func fieldsDeclared(c *valueCtx) []Diagnostic {
	var out []Diagnostic

	c.eachTemplate(func(at site, t *ir.Template) {
		for _, r := range t.Refs {
			src := sourceOfRef(r)
			if src == nil || !plainReference(refNames(r)) {
				continue
			}
			res := walkShape(c.shapeOf(at.node, src), src.Path)
			if res.missing == "" {
				continue
			}
			out = append(out, diag(CodeWF012, at.path+"["+r.String()+"]",
				"%s", fieldProblem(res.parent, res.missing)))
		}
	})

	c.eachSelector(func(at site, sel *ir.Selector) {
		if sel.From == nil {
			return
		}
		res := walkShape(c.shapeOf(at.node, sel.From), sel.From.Path)
		if res.missing != "" && plainReference(sourceNames(sel.From)) {
			out = append(out, diag(CodeWF012, at.path+".from",
				"%s", fieldProblem(res.parent, res.missing)))
		}
		field := c.specField(at)
		if field == "" || res.missing != "" {
			return
		}
		// `field:` reads one property of each element, so an as_list-wrapped
		// single value and a list are checked the same way: at the element.
		rec := res.shape.element()
		switch rec.kind {
		case kindUnknown, kindList:
			return
		case kindScalar:
			out = append(out, diag(CodeWF012, at.path+".field",
				"selector field %q reads a %s, which has no fields", field, rec.label()))
		default:
			if rec.schema == nil {
				return
			}
			_, exists, decidable := fieldInSchema(rec.schema, field)
			if !decidable || exists {
				return
			}
			out = append(out, diag(CodeWF012, at.path+".field",
				"selector field %q is not declared by %s (declared: %s)",
				field, rec.label(), declaredFieldList(rec.schema)))
		}
	})
	return out
}

// fieldProblem explains why an access path stopped resolving. The three
// answers are different mistakes: a typo against a declared field list, a
// field read from a list, and a field read from a scalar.
func fieldProblem(parent shape, seg string) string {
	switch parent.kind {
	case kindList:
		if seg == "" {
			return "reference path has an empty segment"
		}
		return "reference path reads field " + strconv.Quote(seg) +
			" of " + parent.label() + "; a list binds fields per item through field:, not in the path"
	case kindScalar:
		return "reference path reads field " + strconv.Quote(seg) +
			" of a " + parent.label() + ", which has no fields"
	default:
		if parent.schema != nil {
			return "reference path reads field " + strconv.Quote(seg) + ", which " +
				parent.label() + " does not declare (declared: " + declaredFieldList(parent.schema) + ")"
		}
		return "reference path reads field " + strconv.Quote(seg) + " of an undeclared shape"
	}
}

// specField returns the `field:` an author wrote at one selector site. The
// site key is the same string ir.Resolve used to build the compiled
// selector's path, so pairing is by construction rather than by guesswork —
// and a graph node whose path no document carries (a hand-built graph, or a
// graph a later stage edited) has no authoring field to report.
func (c *valueCtx) specField(at site) string {
	sn, ok := c.specNode[at.node.Path]
	if !ok || sn == nil {
		return ""
	}
	switch at.key {
	case "when":
		return selectorField(sn.When)
	case "on":
		return selectorField(sn.On)
	case "until":
		return selectorField(sn.Until)
	case "proposals":
		return selectorField(sn.Proposals)
	case "where":
		return selectorField(sn.Where)
	case "candidates":
		return selectorField(sn.Candidates)
	case "over":
		if sn.Over != nil {
			return selectorField(sn.Over.Selector)
		}
		return ""
	}
	if idx, ok := inputsIndex(at.key); ok && idx < len(sn.Inputs) {
		return sn.Inputs[idx].Field
	}
	return ""
}

func selectorField(s *spec.Selector) string {
	if s == nil {
		return ""
	}
	return s.Field
}

// inputsIndex recovers the position from an aggregate input site key.
func inputsIndex(key string) (int, bool) {
	if !strings.HasPrefix(key, "inputs[") || !strings.HasSuffix(key, "]") {
		return 0, false
	}
	i, err := strconv.Atoi(key[len("inputs[") : len(key)-1])
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
}
