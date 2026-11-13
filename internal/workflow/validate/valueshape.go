package validate

import (
	"encoding/json"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// valueKind is the shallow type of a value a reference or a selector reads.
// The vocabulary is deliberately small and explicit: the §5.2 value types
// (Report, Proposal, Settled, Verdict, Plan, Artifact) are opaque records
// that this package cannot see inside, a schema-declared object is a record
// whose fields it can see, and list<T> is the one composite the DSL actually
// binds items from. Anything the graph does not declare is kindUnknown, and
// every rule in this family stays silent about a value it cannot type.
type valueKind uint8

// Value kinds.
const (
	kindUnknown valueKind = iota
	kindList
	kindObject
	kindScalar
	kindRecord
)

func (k valueKind) String() string {
	switch k {
	case kindList:
		return "list"
	case kindObject:
		return "object"
	case kindScalar:
		return "scalar"
	case kindRecord:
		return "value"
	}
	return "unknown"
}

// shape is the shallow type of one value: its kind, a label for messages,
// the inline JSON Schema that governs it when there is one (which is what
// makes field membership decidable), and the element shape when it is a
// list. schema is only set on a record-like shape; a list keeps its element
// schema in elem so that `field:` can be checked per item.
type shape struct {
	kind   valueKind
	name   string
	schema map[string]any
	elem   *shape
}

// label names a shape in a message, spelling list<T> the way the plan does.
func (s shape) label() string {
	switch {
	case s.kind == kindUnknown:
		return "an undeclared value"
	case s.kind == kindList:
		if s.elem != nil && s.elem.kind != kindUnknown {
			return "list<" + s.elem.label() + ">"
		}
		return "list"
	case s.name != "":
		return s.name
	default:
		return s.kind.String()
	}
}

// notList reports whether the shape is definitely not a list: unknown is
// never definite, which is the whole reason the type rules are shallow.
func (s shape) notList() bool {
	return s.kind != kindUnknown && s.kind != kindList
}

// element is the shape of one item of a list, or of the value itself when it
// is not a list at all.
func (s shape) element() shape {
	if s.kind == kindList && s.elem != nil {
		return *s.elem
	}
	return s
}

// inlineSchema parses the one form of OutputSpec.Schema this family can read
// structure out of: an inline JSON Schema object. A named schema refers to a
// registry this package does not hold, and a malformed schema is
// outputSpecs' WF005 finding, so both decode to nil here and the value rules
// stay quiet about anything they cannot see.
func inlineSchema(s string) map[string]any {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	doc := decodeJSONObject(trimmed)
	return doc
}

// decodeJSONObject reads the first JSON value in s and keeps it only when it
// is one object. Numbers stay json.Number so a schema stays comparable; a
// schema that does not parse is WF005's finding, not this family's, so the
// value rules simply have nothing to read.
func decodeJSONObject(s string) map[string]any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	obj, _ := v.(map[string]any)
	return obj
}

// shapeOfSchema types one schema object. Only `type` decides the kind; the
// schema itself travels along so field membership stays checkable.
func shapeOfSchema(sch map[string]any) shape {
	if sch == nil {
		return shape{}
	}
	switch schemaClass(sch) {
	case "array":
		out := shape{kind: kindList, name: "list"}
		if items, ok := sch["items"].(map[string]any); ok {
			elem := shapeOfSchema(items)
			if elem.kind != kindUnknown {
				out.elem = &elem
				out.name = "list<" + elem.label() + ">"
			}
		}
		return out
	case "object":
		return shape{kind: kindObject, name: "object", schema: sch}
	case "scalar":
		return shape{kind: kindScalar, name: scalarTypeName(sch)}
	}
	return shape{}
}

// schemaClass classifies a schema's declared type as "array", "object",
// "scalar", or "" when the schema does not commit to one. A union of types
// that span more than one class is "" on purpose: "string or array" does not
// settle whether a value is a list.
func schemaClass(sch map[string]any) string {
	v, ok := sch["type"]
	if !ok {
		return ""
	}
	if name, ok := v.(string); ok {
		return classOfTypeName(name)
	}
	list, ok := v.([]any)
	if !ok {
		return ""
	}
	class := ""
	for _, item := range list {
		name, ok := item.(string)
		if !ok {
			return ""
		}
		c := classOfTypeName(name)
		if c == "" {
			continue
		}
		if class == "" {
			class = c
		} else if class != c {
			return ""
		}
	}
	return class
}

func classOfTypeName(name string) string {
	switch name {
	case "array":
		return "array"
	case "object":
		return "object"
	case "string", "number", "integer", "boolean":
		return "scalar"
	}
	return ""
}

// scalarTypeName names the first scalar in a type declaration, for messages.
func scalarTypeName(sch map[string]any) string {
	switch v := sch["type"].(type) {
	case string:
		return v
	case []any:
		for _, item := range v {
			if name, ok := item.(string); ok && classOfTypeName(name) == "scalar" {
				return name
			}
		}
	}
	return "scalar"
}

// shapeOfParam types a declared workflow parameter. ParamSpec has no element
// type and no schema, so a list parameter is list<unknown> and a map
// parameter is an object whose fields nobody declared.
func shapeOfParam(p ir.Param) shape {
	switch p.Type {
	case ir.ParamList:
		return shape{kind: kindList, name: "list"}
	case ir.ParamMap:
		return shape{kind: kindObject, name: "map"}
	case ir.ParamString, ir.ParamInteger, ir.ParamNumber, ir.ParamBoolean:
		return shape{kind: kindScalar, name: string(p.Type)}
	}
	return shape{}
}

// shapeOfOutput types a step's output: the declared §5.2 value type, refined
// by an inline schema when the author wrote one. A named type with no schema
// is a record whose fields nobody declared, which is the common case in the
// golden corpus and must decide nothing.
func shapeOfOutput(n *ir.Node) shape {
	if n == nil || n.Output == nil {
		return shape{}
	}
	out := shape{}
	if n.Output.Type != "" {
		out = shape{kind: kindRecord, name: string(n.Output.Type)}
	}
	sch := inlineSchema(n.Output.Schema)
	if sch == nil {
		return out
	}
	s := shapeOfSchema(sch)
	if s.kind == kindUnknown {
		// A schema that declares properties but no type still declares a
		// shape: treat it as the record it describes.
		s = shape{kind: kindObject, schema: sch}
	}
	if s.kind == kindObject && out.name != "" {
		s.name = out.name
	}
	if s.kind == kindObject && s.schema == nil {
		s.schema = sch
	}
	return s
}

// shapeOfCollect types `<container>.collect`, which is a list by
// construction. Its element shape is only knowable for the single-node body
// the shorthand writes, because a multi-node body collects whichever of its
// nodes the runtime names; that is left unknown rather than guessed.
func shapeOfCollect(container *ir.Node) shape {
	out := shape{kind: kindList, name: "list"}
	if container == nil || len(container.Body) != 1 {
		return out
	}
	elem := shapeOfOutput(container.Body[0])
	if elem.kind == kindUnknown {
		return out
	}
	out.elem = &elem
	out.name = "list<" + elem.label() + ">"
	return out
}

// shapeOfLiteral types one value written literally in a fan-out source.
// Objects keep no schema, because a literal list of mappings is a payload,
// not a declaration of the fields every item carries.
func shapeOfLiteral(v any) shape {
	switch t := v.(type) {
	case map[string]any:
		return shape{kind: kindObject, name: "object"}
	case []any:
		out := shape{kind: kindList, name: "list"}
		if len(t) > 0 {
			elem := shapeOfLiteral(t[0])
			if elem.kind != kindUnknown {
				out.elem = &elem
			}
		}
		return out
	case string:
		return shape{kind: kindScalar, name: "string"}
	case bool:
		return shape{kind: kindScalar, name: "boolean"}
	case int, int64, float64, float32:
		return shape{kind: kindScalar, name: "number"}
	}
	return shape{}
}

// literalElementShape types the item binding of a literal fan-out: the shared
// shape of every literal, or unknown as soon as they disagree.
func literalElementShape(literals []any) shape {
	if len(literals) == 0 {
		return shape{}
	}
	first := shapeOfLiteral(literals[0])
	if first.kind == kindUnknown {
		return shape{}
	}
	for _, v := range literals[1:] {
		if shapeOfLiteral(v).kind != first.kind {
			return shape{kind: first.kind}
		}
	}
	return first
}

// sourcePath is the access path of a compiled source.
func sourcePath(src *ir.Source) []string {
	if src == nil {
		return nil
	}
	return src.Path
}

// sourceOfRef bridges a compiled template reference onto the selector source
// model, because both read values out of the same three roots and the shape
// rules should not be written twice. It builds a fresh value; it never edits
// the reference or the graph. index and run have no selector analogue and
// return nil.
func sourceOfRef(r *ir.Ref) *ir.Source {
	if r == nil {
		return nil
	}
	switch r.Root {
	case ir.RootParams:
		return &ir.Source{Kind: ir.SourceParams, Name: r.Name, Path: r.Path}
	case ir.RootSteps:
		kind := ir.SourceStepOutput
		if r.Field == "collect" {
			kind = ir.SourceStepCollect
		}
		return &ir.Source{Kind: kind, StepID: r.StepID, Path: r.Path}
	case ir.RootItem:
		return &ir.Source{Kind: ir.SourceItem, Name: r.Name, Path: r.Path}
	}
	return nil
}

// shapeOf types the value a source reads, before its access path is walked.
// An unresolvable source is kindUnknown: referencesResolve reports it, and a
// type rule must not stack a second finding on the same defect.
func (c *valueCtx) shapeOf(n *ir.Node, src *ir.Source) shape {
	if src == nil {
		return shape{}
	}
	switch src.Kind {
	case ir.SourceParams:
		if p, ok := c.g.Params[src.Name]; ok {
			return shapeOfParam(p)
		}
	case ir.SourceStepOutput:
		if target, ok := c.scope(n).steps[src.StepID]; ok {
			return shapeOfOutput(target)
		}
	case ir.SourceStepCollect:
		if target, ok := c.scope(n).steps[src.StepID]; ok {
			return shapeOfCollect(target)
		}
	case ir.SourceItem:
		if owner, ok := c.scope(n).items[src.Name]; ok {
			if owner.Over == nil {
				return shape{}
			}
			if owner.Over.Selector != nil {
				// The item binding is one element of the value the fan-out
				// iterates — the source WITH its access path applied, because
				// `over: {from: steps.plan.output.workstreams}` iterates the
				// list that path names, not the output it hangs off. And never
				// shape.element(): its nil fallback returns the list itself,
				// which would type a per-item value as a collection and make
				// WF012 report field access into the list instead of staying
				// silent about an element shape nobody declared.
				base, missing := c.shapeAt(owner, owner.Over.Selector.From)
				if missing != "" || base.kind != kindList || base.elem == nil {
					return shape{}
				}
				return *base.elem
			}
			return literalElementShape(owner.Over.Literals)
		}
	}
	return shape{}
}

// shapeAt types the value a source with its access path reads. missing names
// the first segment the declared shape cannot supply, and is "" whenever the
// shape runs out of declarations — silence there is the shallow half of
// "shallow but explicit".
func (c *valueCtx) shapeAt(n *ir.Node, src *ir.Source) (shape, string) {
	out := walkShape(c.shapeOf(n, src), sourcePath(src))
	return out.shape, out.missing
}

// pathResult is the outcome of walking an access path: the shape the path
// resolves to, and, when it stops early, the segment that did not resolve and
// the shape it was read from. The parent is what lets WF012 tell "field not
// in the declared schema" apart from "a list has no named fields", which are
// different mistakes to fix.
type pathResult struct {
	shape   shape
	missing string
	parent  shape
}

// walkShape narrows a shape along a dotted access path.
func walkShape(sh shape, path []string) pathResult {
	for _, seg := range path {
		if seg == "" {
			return pathResult{missing: seg, parent: sh}
		}
		switch sh.kind {
		case kindObject, kindRecord:
			if sh.schema == nil {
				return pathResult{}
			}
			sub, exists, decidable := fieldInSchema(sh.schema, seg)
			if !decidable {
				return pathResult{}
			}
			if !exists {
				return pathResult{missing: seg, parent: sh}
			}
			sh = shapeOfSchema(sub)
		case kindList, kindScalar:
			// A list binds its fields per item through `field:`, and a
			// scalar has none: naming a field on either is a shape error.
			return pathResult{missing: seg, parent: sh}
		default:
			return pathResult{}
		}
	}
	return pathResult{shape: sh}
}

// fieldInSchema looks one property up in a declared object schema. The three
// results matter separately: a field the schema declares, a field the schema
// rules out, and a schema that never committed either way.
//
// A schema that declares `properties` is read as the author's field list.
// Plain JSON Schema would allow unlisted extra properties unless
// `additionalProperties: false` says otherwise, but "the reference names a
// field the declared shape does not have" is the typo this rule exists to
// catch, and treating a declared shape as open would let every one of them
// through.
func fieldInSchema(sch map[string]any, name string) (sub map[string]any, exists, decidable bool) {
	props, _ := sch["properties"].(map[string]any)
	if prop, ok := props[name].(map[string]any); ok {
		return prop, true, true
	}
	if len(props) > 0 {
		return nil, false, true
	}
	ap, ok := sch["additionalProperties"]
	if !ok {
		return nil, false, false
	}
	if b, isBool := ap.(bool); isBool {
		if b {
			return nil, false, false
		}
		return nil, false, true
	}
	if obj, isSchema := ap.(map[string]any); isSchema {
		return obj, true, true
	}
	return nil, false, false
}
