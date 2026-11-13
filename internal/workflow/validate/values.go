package validate

import (
	"fmt"
	"sort"

	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// Rule codes for the value and template family. Like the structural codes,
// these strings are part of the document contract: adding a rule means a new
// constant here and a row in the table in doc.go.
//
// WF014 is not in the original plan catalog, which reserved WF010–WF013 for
// "value and template validation". It joins the family because a payload
// field on the wrong node kind is the same failure one step earlier: the
// author wrote a value at a place the graph cannot carry it, so nothing can
// ever read it back. It is appended rather than folded into WF011 so that
// "type incompatible" and "field misplaced" stay separately addressable.
const (
	// CodeWF010 marks a compiled reference that names nothing in scope: an
	// undeclared param, a step the node cannot see, a collect on a node that
	// produces none, an item binding or index outside the body that declares
	// it, or a reference with no source at all.
	CodeWF010 = "WF010"
	// CodeWF011 marks a producer/consumer type mismatch: a consumer that
	// needs a list reading a value declared as something else, a consumer
	// that needs one value reading a list, an element type a settle cannot
	// act on, and an output whose declared value type contradicts its own
	// declared schema.
	CodeWF011 = "WF011"
	// CodeWF012 marks a field access the declared shape cannot supply: a
	// template or selector path into a step output that the output's schema
	// does not declare, a field read off a list or a scalar, and a selector
	// `field:` naming a property the element schema does not declare.
	CodeWF012 = "WF012"
	// CodeWF013 marks a value that cannot be produced from itself: a node
	// reading its own output, a body reading the collect of the fan-out that
	// contains it, a reference written as a call or a filter, and a template
	// whose compiled segments do not match its compiled references.
	CodeWF013 = "WF013"
	// CodeWF014 marks a payload field that belongs to another node kind,
	// which the resolver drops: the declaration can have no effect, so the
	// author believes a value is set that never reaches the graph.
	CodeWF014 = "WF014"
)

// Values runs the value and template family (WF010–WF014) over a resolved
// graph and returns every finding, in the same order contract Structural
// documents: rule by rule in code order, and within a rule in document
// order with a fixed field order per node.
//
// The graph alone carries almost everything this family needs, because the
// resolver already compiled the templates and selectors. doc is the
// authoring document the graph came from, and it is a parameter for exactly
// one reason: ir.compileSelector drops Selector.Field (bead yaah-0u0v.32),
// so the `field:` a settle or loop author wrote exists only in the document.
// A nil doc means the graph was built or edited outside the pipeline, and
// the checks that need authoring syntax stay silent — every other check in
// the family still runs.
//
// Like Structural, this takes no position on whether the graph was built
// honestly: a graph assembled by hand or by a later stage is walked the same
// way, and the compiled forms are treated as claims to re-check rather than
// as guarantees.
func Values(doc *spec.Document, g *ir.Graph) []Diagnostic {
	if g == nil {
		return nil
	}
	c := newValueCtx(doc, g)
	var out []Diagnostic
	out = append(out, referencesResolve(c)...)
	out = append(out, typesCompatible(c)...)
	out = append(out, fieldsDeclared(c)...)
	out = append(out, templatesNotRecursive(c)...)
	out = append(out, payloadsMatchKind(c)...)
	return out
}

// ------------------------------------------------------------------- context

// valueCtx holds the scope tables the family shares. They are built once per
// Values call and never mutate the graph.
type valueCtx struct {
	g      *ir.Graph
	order  []*ir.Node
	parent map[*ir.Node]*ir.Node
	// top indexes top-level step ids, the part of the reference namespace
	// that is visible everywhere.
	top      map[string]*ir.Node
	scopes   map[*ir.Node]*nodeScope
	specNode map[string]*spec.Node
}

// nodeScope is the lexical scope one node's values are written in: the step
// ids it may name and the item bindings it may read, both innermost-first
// for a body-local name.
type nodeScope struct {
	steps map[string]*ir.Node
	items map[string]*ir.Node
}

func newValueCtx(doc *spec.Document, g *ir.Graph) *valueCtx {
	order, parent := graphNodes(g)
	c := &valueCtx{
		g:        g,
		order:    order,
		parent:   parent,
		top:      stepsByID(g),
		scopes:   map[*ir.Node]*nodeScope{},
		specNode: specNodesByPath(doc),
	}
	return c
}

// scope returns the reference namespace visible to one node. The rule it
// reconstructs is ir.resolveBody's: all top-level ids are visible everywhere,
// a body sees the ids of its own siblings up to and including itself, and a
// nested body inherits the scope its container was resolved in — which is why
// the walk climbs outward and applies what it finds innermost-last.
func (c *valueCtx) scope(n *ir.Node) *nodeScope {
	if s, ok := c.scopes[n]; ok {
		return s
	}
	s := &nodeScope{
		steps: make(map[string]*ir.Node, len(c.top)+4),
		items: map[string]*ir.Node{},
	}
	for id, node := range c.top {
		s.steps[id] = node
	}
	for child := n; ; {
		up := c.parent[child]
		if up == nil {
			break
		}
		for _, sib := range visibleSiblings(up, child) {
			if sib == nil || sib.ID == "" {
				continue
			}
			// A body id that collides with a step id is uniqueReferences'
			// finding; keep the top-level binding so this rule does not
			// cascade over the same defect.
			if _, isTop := c.top[sib.ID]; isTop {
				continue
			}
			s.steps[sib.ID] = sib
		}
		child = up
	}
	for up := c.parent[n]; up != nil; up = c.parent[up] {
		if up.Kind == ir.KindFanout && up.As != "" {
			if _, seen := s.items[up.As]; !seen {
				s.items[up.As] = up
			}
		}
	}
	c.scopes[n] = s
	return s
}

// -------------------------------------------------------------------- sites

// site is one place in the graph an author wrote a value: the node that
// holds it, the field that holds it, and the path a diagnostic anchors at.
type site struct {
	node *ir.Node
	key  string
	path string
}

func valueSite(n *ir.Node, key string) site {
	return site{node: n, key: key, path: n.Path + "." + key}
}

// eachTemplate visits every compiled template in the graph, one node's
// fields in a fixed order so emission does not depend on Go map or struct
// iteration. A template field is read wherever it appears: whether the
// field belongs to the kind is payloadsMatchKind' question, not this walk's.
func (c *valueCtx) eachTemplate(fn func(site, *ir.Template)) {
	for _, n := range c.order {
		if n == nil {
			continue
		}
		if n.Prompt != nil {
			fn(valueSite(n, "prompt"), n.Prompt)
		}
		if n.SeedFrom != nil {
			fn(valueSite(n, "seed_from"), n.SeedFrom)
		}
		if n.ContinueFrom != nil {
			fn(valueSite(n, "continue_from"), n.ContinueFrom)
		}
		if n.ForkFrom != nil {
			fn(valueSite(n, "fork_from"), n.ForkFrom)
		}
		if n.Value != nil {
			fn(valueSite(n, "value"), n.Value)
		}
		if n.Judge != nil && n.Judge.Prompt != nil {
			fn(valueSite(n, "judge.prompt"), n.Judge.Prompt)
		}
		for _, k := range sortedParamNames(n.CallParams) {
			if pv := n.CallParams[k]; pv.Template != nil {
				fn(valueSite(n, "params."+k), pv.Template)
			}
		}
	}
}

// eachSelector visits every compiled selector in the graph, in the same
// fixed order.
func (c *valueCtx) eachSelector(fn func(site, *ir.Selector)) {
	for _, n := range c.order {
		if n == nil {
			continue
		}
		if n.When != nil {
			fn(valueSite(n, "when"), n.When)
		}
		if n.On != nil {
			fn(valueSite(n, "on"), n.On)
		}
		if n.Until != nil {
			fn(valueSite(n, "until"), n.Until)
		}
		if n.Over != nil && n.Over.Selector != nil {
			fn(valueSite(n, "over"), n.Over.Selector)
		}
		if n.Proposals != nil {
			fn(valueSite(n, "proposals"), n.Proposals)
		}
		if n.Where != nil {
			fn(valueSite(n, "where"), n.Where)
		}
		if n.Candidates != nil {
			fn(valueSite(n, "candidates"), n.Candidates)
		}
		for i := range n.Inputs {
			if n.Inputs[i] != nil {
				fn(valueSite(n, fmt.Sprintf("inputs[%d]", i)), n.Inputs[i])
			}
		}
	}
}

// sortedParamNames keeps call-parameter diagnostics deterministic: the
// resolved graph holds them in a map.
func sortedParamNames(m map[string]ir.ParamValue) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// specNodesByPath indexes the authoring nodes by the path ir.Resolve gives
// them, so a graph node can be paired back with what the author wrote.
// Collisions cannot happen in a resolved graph; the first binder wins so a
// hand-edited path cannot pick a winner by map order.
func specNodesByPath(doc *spec.Document) map[string]*spec.Node {
	out := map[string]*spec.Node{}
	if doc == nil {
		return out
	}
	var walk func(nodes []spec.Node, at func(i int, n *spec.Node) string)
	walk = func(nodes []spec.Node, at func(i int, n *spec.Node) string) {
		for i := range nodes {
			n := &nodes[i]
			path := at(i, n)
			if _, dup := out[path]; !dup {
				out[path] = n
			}
			if len(n.Body) > 0 {
				body := n.Body
				walk(body, func(j int, _ *spec.Node) string {
					return fmt.Sprintf("%s.body[%d]", path, j)
				})
			}
		}
	}
	walk(doc.Spec.Steps, func(_ int, n *spec.Node) string { return "steps." + n.ID })
	return out
}

// visibleSiblings is the part of a container's body a child may name: the
// child itself and everything before it, because ir.resolveBody registers a
// local id immediately before resolving the node that carries it.
//
// The index is found by pointer identity rather than remembered, and an
// absent child yields the whole body. A graph can alias one node into two
// containers — graphNodes documents why that is assumed, not ruled out — and
// a remembered index would then slice the wrong body: a panic in a validator
// is a denial of service in the CLI, and a wide scope only ever hides a
// finding rather than inventing one.
func visibleSiblings(container, child *ir.Node) []*ir.Node {
	for i, b := range container.Body {
		if b == child {
			return container.Body[:i+1]
		}
	}
	return container.Body
}

// -------------------------------------------------------------------- WF010

// referencesResolve checks WF010: every compiled reference resolves in scope.
//
// ir.Resolve already rejects the authoring forms of most of these defects —
// an undeclared param, an out-of-scope step, `steps.x.status`, a collect on
// a node that has none, `index` with no binding in scope, a deeper path under
// run.id — so a document that came through Validate reaches here clean and
// surfaced the failure earlier as WF001. What this rule owns is the residue
// on the other side of that seam, in the same shape WF002 and WF005 own:
//
//   - The graph is exported and hand-buildable. A scheduler, a replay, or a
//     later bead can hand Values a Template whose Refs were assembled
//     directly, and no resolver ran over them. Reference existence is a
//     property of the graph, so the graph checks it.
//   - Resolve reports the first bad reference in the first bad template; this
//     walk reports every one of them across the whole document.
//   - Scope is a graph relation. Resolve checked a ref against the lexical
//     scope it happened to be holding mid-walk; this rule re-derives the
//     scope from the tree, which is the only version of the check that stays
//     true after a graph is edited.
func referencesResolve(c *valueCtx) []Diagnostic {
	var out []Diagnostic
	c.eachTemplate(func(s site, t *ir.Template) {
		for _, r := range t.Refs {
			if r == nil {
				out = append(out, diag(CodeWF010, s.path,
					"template has a reference slot that was never compiled"))
				continue
			}
			out = append(out, c.refProblems(s.node, r, s.path+"["+r.String()+"]")...)
		}
	})
	c.eachSelector(func(s site, sel *ir.Selector) {
		if sel.From == nil {
			out = append(out, diag(CodeWF010, s.path+".from", "selector has no source"))
			return
		}
		out = append(out, c.sourceProblems(s.node, sel.From, s.path+".from")...)
	})
	return out
}

// refProblems re-resolves one compiled template reference. at is the
// diagnostic anchor, which names the segment as well as the field.
func (c *valueCtx) refProblems(n *ir.Node, r *ir.Ref, at string) []Diagnostic {
	var out []Diagnostic
	switch r.Root {
	case ir.RootParams:
		out = append(out, c.paramProblems(r.Name, at, r.Path)...)
	case ir.RootSteps:
		out = append(out, c.stepProblems(n, r.StepID, r.Field, at, r.Path)...)
	case ir.RootItem:
		out = append(out, c.itemProblems(n, r.Name, at, r.Path)...)
	case ir.RootIndex:
		if len(c.scope(n).items) == 0 {
			out = append(out, diag(CodeWF010, at, "index is only in scope inside a fan-out body"))
		}
		if len(r.Path) > 0 {
			out = append(out, diag(CodeWF010, at, "index is a number and has no fields"))
		}
	case ir.RootRun:
		if r.Field != "id" && r.Field != "attempt" {
			out = append(out, diag(CodeWF010, at, "run reference must be run.id or run.attempt, not %q", r.Field))
		}
		if len(r.Path) > 0 {
			out = append(out, diag(CodeWF010, at, "run fields have no deeper path"))
		}
	default:
		out = append(out, diag(CodeWF010, at, "reference has an unknown root %q", string(r.Root)))
	}
	return out
}

// sourceProblems re-resolves one compiled selector source.
func (c *valueCtx) sourceProblems(n *ir.Node, src *ir.Source, at string) []Diagnostic {
	var out []Diagnostic
	switch src.Kind {
	case ir.SourceParams:
		out = append(out, c.paramProblems(src.Name, at, src.Path)...)
	case ir.SourceStepOutput:
		out = append(out, c.stepProblems(n, src.StepID, "output", at, src.Path)...)
	case ir.SourceStepCollect:
		out = append(out, c.stepProblems(n, src.StepID, "collect", at, src.Path)...)
	case ir.SourceItem:
		out = append(out, c.itemProblems(n, src.Name, at, src.Path)...)
	default:
		out = append(out, diag(CodeWF010, at, "selector source has an unknown kind %q", string(src.Kind)))
	}
	return out
}

func (c *valueCtx) paramProblems(name string, at string, path []string) []Diagnostic {
	var out []Diagnostic
	switch {
	case name == "":
		out = append(out, diag(CodeWF010, at, "parameter reference names no parameter"))
	default:
		if _, ok := c.g.Params[name]; !ok {
			out = append(out, diag(CodeWF010, at, "names an undeclared param %q", name))
		}
	}
	return append(out, pathProblems(path, at)...)
}

// stepProblems checks a steps.<id>.<field> reference against the node's own
// scope. Whether the field exists in the step's declared shape is
// fieldsDeclared' question; this only decides whether the reference has a
// referent.
func (c *valueCtx) stepProblems(n *ir.Node, stepID, field, at string, path []string) []Diagnostic {
	var out []Diagnostic
	switch {
	case stepID == "":
		out = append(out, diag(CodeWF010, at, "step reference names no step"))
	default:
		target, ok := c.scope(n).steps[stepID]
		switch {
		case !ok:
			out = append(out, diag(CodeWF010, at, "names the step %q, which is not in scope", stepID))
		case field != "output" && field != "collect":
			out = append(out, diag(CodeWF010, at, "must read output or collect, not %q", field))
		case field == "collect" && target.Collect == "":
			out = append(out, diag(CodeWF010, at, "reads collect from step %q, which produces none", stepID))
		}
	}
	return append(out, pathProblems(path, at)...)
}

func (c *valueCtx) itemProblems(n *ir.Node, name, at string, path []string) []Diagnostic {
	var out []Diagnostic
	switch {
	case name == "":
		out = append(out, diag(CodeWF010, at, "item reference names no binding"))
	default:
		if _, ok := c.scope(n).items[name]; !ok {
			out = append(out, diag(CodeWF010, at, "names the item binding %q, which is not in scope", name))
		}
	}
	return append(out, pathProblems(path, at)...)
}

// pathProblems reports an empty access segment, which no renderer can
// resolve and which the dotted source form cannot express.
func pathProblems(path []string, at string) []Diagnostic {
	var out []Diagnostic
	for _, seg := range path {
		if seg == "" {
			out = append(out, diag(CodeWF010, at, "reference path has an empty segment"))
		}
	}
	return out
}
