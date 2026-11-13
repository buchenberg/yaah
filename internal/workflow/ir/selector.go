package ir

import (
	"encoding/json"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// SourceKind enumerates what a selector reads from.
type SourceKind string

// Selector source kinds.
const (
	SourceParams      SourceKind = "params"
	SourceStepOutput  SourceKind = "step_output"
	SourceStepCollect SourceKind = "step_collect"
	SourceItem        SourceKind = "item"
)

// Source is a compiled selector source: a root binding plus a dotted
// access path.
type Source struct {
	Kind   SourceKind `json:"kind"`
	Name   string     `json:"name,omitempty"` // params name or item as-name
	StepID string     `json:"step,omitempty"`
	Path   []string   `json:"path,omitempty"`
}

// String reconstructs the dotted source form.
func (s *Source) String() string {
	var parts []string
	switch s.Kind {
	case SourceParams:
		parts = append([]string{"params", s.Name}, s.Path...)
	case SourceStepOutput:
		parts = append([]string{"steps", s.StepID, "output"}, s.Path...)
	case SourceStepCollect:
		parts = append([]string{"steps", s.StepID, "collect"}, s.Path...)
	case SourceItem:
		parts = append([]string{s.Name}, s.Path...)
	}
	return strings.Join(parts, ".")
}

// PredOp enumerates selector predicates (§5.4). A selector carries at most
// one.
type PredOp string

// Predicate operations.
const (
	PredNone      PredOp = ""
	PredEquals    PredOp = "equals"
	PredNotEquals PredOp = "not_equals"
	PredIn        PredOp = "in"
	PredNotEmpty  PredOp = "not_empty"
	PredExists    PredOp = "exists"
)

// Predicate is a compiled selector predicate: the operator, its literal
// argument, and the element property the operator reads.
type Predicate struct {
	Op  PredOp `json:"op,omitempty"`
	Arg any    `json:"arg,omitempty"`
	// Field is the authoring `field:` key: the property of each element the
	// predicate compares. It lives here rather than on Selector because it
	// is the left operand of the comparison, not a projection of the result
	// — `where: {from: xs, field: verdict, equals: pass}` keeps the elements
	// of xs whose verdict is pass, so the value the selector yields is still
	// the element. An empty Field compares the element itself. A set Field
	// with an empty Op is a retention filter in the same shape, not a
	// projection: the executor keeps the elements whose field is set and
	// still yields the elements — the clause is shorthand for an
	// `exists`/`not_empty` test on the field. A selector never changes the
	// kind of value it yields with the operator's presence, because
	// consumers like `settle.proposals` and `fanout.over` read elements
	// uniformly; the executor (yaah-0u0v.11) and WF023 both rely on this
	// one answer.
	//
	// compileSelector used to drop it (yaah-0u0v.32), which made a filtered
	// selector silently compare the whole element against the literal.
	// Shape checking of a field against the source's element schema stays
	// with WF012, which reads the document too: the IR has no shape model,
	// and rejecting here would turn a value finding into a resolution
	// failure.
	Field string `json:"field,omitempty"`
}

// Selector is a compiled structured selector: a source, at most one
// predicate, and the as_list wrapper flag.
type Selector struct {
	From      *Source   `json:"from"`
	Predicate Predicate `json:"predicate,omitempty"`
	AsList    bool      `json:"as_list,omitempty"`
}

// MarshalJSON omits an absent predicate so unconditional selectors
// serialize compactly. A `field:` with no operator is still a predicate
// clause — the element-wise retention-filter shorthand pinned in the
// Predicate.Field doc — so the compact form keys on either half being set.
func (s *Selector) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	obj := map[string]any{"from": s.From}
	if s.Predicate.Op != PredNone {
		obj["op"] = s.Predicate.Op
		if s.Predicate.Arg != nil {
			obj["arg"] = s.Predicate.Arg
		}
	}
	if s.Predicate.Field != "" {
		obj["field"] = s.Predicate.Field
	}
	if s.AsList {
		obj["as_list"] = true
	}
	return json.Marshal(obj)
}

// compileSelector compiles an authoring selector into the AST, checking
// the source against the lexical scope and validating predicate types.
func compileSelector(path string, in *spec.Selector, lex *lexscope) (*Selector, error) {
	if in == nil {
		return nil, nil
	}
	src, err := compileSource(path+".from", in.From, lex)
	if err != nil {
		return nil, err
	}
	out := &Selector{From: src, AsList: in.AsList != nil && *in.AsList}
	// The element property the predicate reads travels with the predicate.
	// This assignment is the whole of yaah-0u0v.32: the field was decoded
	// and then never read, so a filtered selector compared the element.
	out.Predicate.Field = in.Field

	var ops []PredOp
	set := func(op PredOp) { ops = append(ops, op) }
	if in.Equals != nil {
		set(PredEquals)
	}
	if in.NotEquals != nil {
		set(PredNotEquals)
	}
	if in.In != nil {
		set(PredIn)
	}
	if in.NotEmpty != nil {
		set(PredNotEmpty)
	}
	if in.Exists != nil {
		set(PredExists)
	}
	switch {
	case len(ops) == 0:
	case len(ops) == 1:
		out.Predicate.Op = ops[0]
	default:
		names := make([]string, 0, len(ops))
		for _, op := range ops {
			names = append(names, string(op))
		}
		return nil, resolveErrf(path, "selector declares %d predicates (%s); at most one is allowed", len(ops), strings.Join(names, ", "))
	}

	// Predicate argument type checks: static mismatches are rejected here
	// rather than at run time.
	switch out.Predicate.Op {
	case PredEquals, PredNotEquals:
		if _, ok := normalizePredicateArg(in.Equals, in.NotEquals); !ok {
			return nil, resolveErrf(path, "predicate %s requires a scalar literal", out.Predicate.Op)
		}
		out.Predicate.Arg = predicateArg(in.Equals, in.NotEquals)
	case PredIn:
		if in.In == nil {
			return nil, resolveErrf(path, "predicate in requires a list of literals")
		}
		out.Predicate.Arg = in.In
	case PredNotEmpty:
		if in.NotEmpty == nil {
			return nil, resolveErrf(path, "predicate not_empty requires a boolean")
		}
		out.Predicate.Arg = *in.NotEmpty
	case PredExists:
		if in.Exists == nil {
			return nil, resolveErrf(path, "predicate exists requires a boolean")
		}
		out.Predicate.Arg = *in.Exists
	}
	return out, nil
}

func predicateArg(equals, notEquals any) any {
	if equals != nil {
		return equals
	}
	return notEquals
}

// normalizePredicateArg reports whether an equals/not_equals literal is a
// scalar. Mappings and lists are not matchable literals in v1.
func normalizePredicateArg(args ...any) (any, bool) {
	for _, a := range args {
		if a == nil {
			continue
		}
		switch a.(type) {
		case map[string]any, []any:
			return nil, false
		}
		return a, true
	}
	return nil, true
}

// compileSource parses a selector `from` path against the lexical scope.
func compileSource(path, from string, lex *lexscope) (*Source, error) {
	parts := strings.Split(from, ".")
	for _, p := range parts {
		if p == "" {
			return nil, resolveErrf(path, "selector path %q has an empty segment", from)
		}
	}
	head := parts[0]
	rest := parts[1:]
	switch head {
	case "params":
		if len(rest) == 0 {
			return nil, resolveErrf(path, "selector path %q must name a parameter", from)
		}
		if _, ok := lex.params[rest[0]]; !ok {
			return nil, resolveErrf(path, "selector path %q names an undeclared param %q", from, rest[0])
		}
		return &Source{Kind: SourceParams, Name: rest[0], Path: rest[1:]}, nil
	case "steps":
		if len(rest) < 2 {
			return nil, resolveErrf(path, "selector path %q must be steps.<id>.output or steps.<id>.collect", from)
		}
		info, ok := lex.steps[rest[0]]
		if !ok {
			return nil, resolveErrf(path, "selector path %q names an out-of-scope step %q", from, rest[0])
		}
		field := rest[1]
		if field != "output" && field != "collect" {
			return nil, resolveErrf(path, "selector path %q must use output or collect, not %q", from, field)
		}
		if field == "collect" && !info.hasCollect {
			return nil, resolveErrf(path, "selector path %q: step %q is a %s and has no collect", from, rest[0], info.kind)
		}
		kind := SourceStepOutput
		if field == "collect" {
			kind = SourceStepCollect
		}
		return &Source{Kind: kind, StepID: rest[0], Path: rest[2:]}, nil
	case "index", "run":
		return nil, resolveErrf(path, "selector path %q cannot bind %s; selectors read values, not loop or run state", from, head)
	}
	name := head
	if head == "item" {
		if len(lex.items) == 0 {
			return nil, resolveErrf(path, "selector path %q: no item binding in scope", from)
		}
		name = lex.items[len(lex.items)-1]
	}
	for _, binding := range lex.items {
		if binding == name {
			return &Source{Kind: SourceItem, Name: name, Path: rest}, nil
		}
	}
	if head == "item" {
		return nil, resolveErrf(path, "selector path %q: no item binding in scope", from)
	}
	return nil, resolveErrf(path, "unknown selector root %q in %q", head, from)
}
