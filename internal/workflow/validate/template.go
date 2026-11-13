package validate

import (
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// templatesNotRecursive checks WF013: no template recursion, no function
// calls.
//
// ir.Resolve owns the authoring forms of both halves — it refuses a nested
// `{{`, an empty `{{ }}`, and an unterminated opener while compiling, and its
// reference grammar has no production for a call, so `params.x | upper`
// cannot resolve at all. What the compiled forms still allow, and what this
// rule therefore owns, is narrower and real:
//
//   - Self-value recursion. A dotted grammar resolves a reference to a step
//     that happens to be the node holding it, because every top-level id is
//     visible everywhere and a body-local id is registered before its own
//     node resolves (§5.3). `prompt: "… {{ steps.fix.output.diff }}"` inside
//     step fix is a well-formed compiled reference that can never be
//     rendered: the value it reads is the value the read happens in.
//     Reading a step's own `collect` is legal and is not reported —
//     ir/testdata/specs/bounded_repair.yaml terminates its loop with
//     `until: {from: steps.repair.collect}`, which is the accumulation the
//     loop owns. What is not legal is a body reading the collect of the
//     fan-out that contains it: that list is the body's output, so the body
//     would have to consume what it has not yet produced.
//   - Calls written as path segments. The dotted grammar silently accepts a
//     segment that looks like an invocation: `steps.plan.output.top(x)`
//     compiles to a path segment "top(x)" against a declared field list that
//     cannot contain parentheses. Rather than re-parse the source — the
//     family's design constraint — the rule asserts that every compiled name
//     is a plain identifier, which is exactly the claim the grammar makes.
//   - Templates that were never compiled. Segments and Refs are two views of
//     one render program, and only a compiled template keeps them in step.
//     A literal segment holding `{{`, a slot holding both a literal and a
//     reference, a rendered reference that is not declared, and a declared
//     reference that is never rendered each describe a template no compiler
//     produced.
func templatesNotRecursive(c *valueCtx) []Diagnostic {
	var out []Diagnostic
	c.eachTemplate(func(at site, t *ir.Template) {
		out = append(out, templateIntegrity(at, t)...)
		for _, r := range t.Refs {
			if r == nil {
				continue
			}
			out = append(out, callSyntax(at.path+"["+r.String()+"]", refNames(r))...)
			out = append(out, c.valueRecursion(at.node, at.path+"["+r.String()+"]", sourceOfRef(r))...)
		}
	})
	c.eachSelector(func(at site, sel *ir.Selector) {
		if sel.From == nil {
			return
		}
		out = append(out, callSyntax(at.path+".from", sourceNames(sel.From))...)
		out = append(out, c.valueRecursion(at.node, at.path+".from", sel.From)...)
	})
	return out
}

// templateIntegrity checks one compiled template against itself.
func templateIntegrity(at site, t *ir.Template) []Diagnostic {
	var out []Diagnostic
	if strings.TrimSpace(t.Raw) != "" && len(t.Segments) == 0 {
		out = append(out, diag(CodeWF013, at.path,
			"template has raw text but no compiled segments"))
	}
	declared := make(map[*ir.Ref]bool, len(t.Refs))
	for _, r := range t.Refs {
		if r != nil {
			declared[r] = true
		}
	}
	rendered := make(map[*ir.Ref]bool, len(t.Segments))
	for i, seg := range t.Segments {
		where := at.path + ".segment[" + itoa(i) + "]"
		switch {
		case seg.Ref == nil && seg.Literal == "":
			out = append(out, diag(CodeWF013, where, "template segment is empty"))
		case seg.Ref != nil && seg.Literal != "":
			out = append(out, diag(CodeWF013, where,
				"segment is both literal text and a reference"))
		case seg.Ref == nil:
			if strings.Contains(seg.Literal, "{{") {
				out = append(out, diag(CodeWF013, where,
					"literal segment still holds a {{ opener; a compiled template never renders one"))
			}
			continue
		}
		rendered[seg.Ref] = true
		if !declared[seg.Ref] {
			out = append(out, diag(CodeWF013, where,
				"template renders the reference %q without declaring it", seg.Ref.String()))
		}
	}
	for _, r := range t.Refs {
		if r != nil && !rendered[r] {
			out = append(out, diag(CodeWF013, at.path+"["+r.String()+"]",
				"template declares a reference it never renders"))
		}
	}
	return out
}

// valueRecursion reports a reference that reads a value the reading node
// cannot have yet. Identity is compared by node pointer, not by id, so a
// graph with duplicate ids — which is uniqueReferences' finding — does not
// turn every reference into a cycle.
func (c *valueCtx) valueRecursion(n *ir.Node, at string, src *ir.Source) []Diagnostic {
	if src == nil || n == nil {
		return nil
	}
	var field string
	switch src.Kind {
	case ir.SourceStepOutput:
		field = "output"
	case ir.SourceStepCollect:
		field = "collect"
	default:
		return nil
	}
	target, ok := c.scope(n).steps[src.StepID]
	if !ok || target == nil {
		return nil
	}
	if target == n {
		if field == "output" {
			return []Diagnostic{diag(CodeWF013, at,
				"reads its own output, which does not exist until this node has produced it")}
		}
		return nil
	}
	if field == "collect" && target.Kind == ir.KindFanout && c.isAncestor(target, n) {
		return []Diagnostic{diag(CodeWF013, at,
			"reads the collect of the fan-out that contains it: the body produces that list, so it cannot also consume it")}
	}
	return nil
}

// isAncestor reports whether container holds n, at any depth.
func (c *valueCtx) isAncestor(container, n *ir.Node) bool {
	for up := c.parent[n]; up != nil; up = c.parent[up] {
		if up == container {
			return true
		}
	}
	return false
}

// callSyntax reports the first compiled name that is not a plain identifier.
// One finding per reference is enough: an author who wrote a filter made one
// mistake, not one per segment.
func callSyntax(at string, names []string) []Diagnostic {
	for _, name := range names {
		if name == "" || plainName(name) {
			continue
		}
		return []Diagnostic{diag(CodeWF013, at,
			"reference segment %q is not a plain name: templates and selectors resolve dotted paths only, with no functions, filters, or calls",
			truncated(name, 40))}
	}
	return nil
}

// refNames and sourceNames are the identifiers a compiled reference is built
// from. Field is left out on purpose: WF010 requires it to be one of two
// exact literals, so a call-shaped field is already reported there.
func refNames(r *ir.Ref) []string {
	names := []string{r.Name, r.StepID}
	return append(names, r.Path...)
}

func sourceNames(src *ir.Source) []string {
	names := []string{src.Name, src.StepID}
	return append(names, src.Path...)
}

// plainReference reports whether every name a reference is built from is a
// plain identifier. The type and schema rules use it to stay quiet about a
// path WF013 already called a non-path, so one typo produces one finding.
func plainReference(names []string) bool {
	for _, name := range names {
		if name == "" {
			continue
		}
		if !plainName(name) {
			return false
		}
	}
	return true
}

// plainName accepts the identifier grammar the DSL's dotted paths use: ASCII
// letters, digits, and the two separators authors actually write. Anything
// outside it — parentheses, pipes, quotes, spaces — is call or filter syntax
// that has no place in a resolved reference.
func plainName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// itoa keeps segment anchors short and allocation-free for the small counts
// a template holds.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for i > 0 {
		pos--
		digits[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(digits[pos:])
}
