package ir

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Root enumerates the allowed template reference roots (§5.4).
type Root string

// Template roots.
const (
	RootParams Root = "params"
	RootSteps  Root = "steps"
	RootItem   Root = "item"
	RootIndex  Root = "index"
	RootRun    Root = "run"
)

// Ref is a compiled template reference: a root, an optional binding or
// step id, a field, and a dotted access path into the bound value.
type Ref struct {
	Root   Root     `json:"root"`
	Name   string   `json:"name,omitempty"`  // params name or item as-name
	StepID string   `json:"step,omitempty"`  // steps root: step id
	Field  string   `json:"field,omitempty"` // steps: output|collect; run: id|attempt
	Path   []string `json:"path,omitempty"`
}

// String reconstructs the dotted source form of the reference.
func (r *Ref) String() string {
	if r == nil {
		return ""
	}
	var parts []string
	switch r.Root {
	case RootParams:
		parts = append([]string{"params", r.Name}, r.Path...)
	case RootSteps:
		parts = append([]string{"steps", r.StepID, r.Field}, r.Path...)
	case RootItem:
		parts = append([]string{r.Name}, r.Path...)
	case RootIndex:
		parts = []string{"index"}
	case RootRun:
		parts = append([]string{"run", r.Field}, r.Path...)
	}
	return strings.Join(parts, ".")
}

// Segment is one piece of a compiled template: a literal run or a
// reference. Exactly one side is set.
type Segment struct {
	Literal string
	Ref     *Ref
}

// Template is a compiled template. Raw is the original source; Refs lists
// the distinct references in first-appearance order; Segments is the
// render program. Segments are excluded from serialization: raw plus refs
// is the diffable golden form.
type Template struct {
	Raw      string    `json:"raw"`
	Refs     []*Ref    `json:"refs,omitempty"`
	Segments []Segment `json:"-"`
}

// compileTemplate compiles a template string against the lexical scope.
// An empty raw compiles to nil (no template). Strings without {{ }}
// compile to a single literal segment.
func compileTemplate(path, raw string, lex *lexscope) (*Template, error) {
	if raw == "" {
		return nil, nil
	}
	t := &Template{Raw: raw}
	seen := map[string]bool{}
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			t.Segments = append(t.Segments, Segment{Literal: b.String()})
			b.Reset()
		}
	}
	for i := 0; i < len(raw); {
		if !strings.HasPrefix(raw[i:], "{{") {
			b.WriteByte(raw[i])
			i++
			continue
		}
		end := strings.Index(raw[i+2:], "}}")
		if end < 0 {
			return nil, resolveErrf(path, "template has an unterminated {{ at offset %d", i)
		}
		expr := strings.TrimSpace(raw[i+2 : i+2+end])
		if expr == "" {
			return nil, resolveErrf(path, "template has an empty {{ }} at offset %d", i)
		}
		if strings.Contains(expr, "{{") {
			return nil, resolveErrf(path, "template has nested {{ at offset %d; templates do not nest", i)
		}
		ref, err := compileRef(path, expr, lex)
		if err != nil {
			return nil, err
		}
		flush()
		t.Segments = append(t.Segments, Segment{Ref: ref})
		key := ref.String()
		if !seen[key] {
			seen[key] = true
			t.Refs = append(t.Refs, ref)
		}
		i += 2 + end + 2
	}
	flush()
	return t, nil
}

// compileRef parses one dotted expression against the lexical scope.
func compileRef(path, expr string, lex *lexscope) (*Ref, error) {
	parts := strings.Split(expr, ".")
	head := parts[0]
	rest := parts[1:]
	for _, p := range parts {
		if p == "" {
			return nil, resolveErrf(path, "template reference %q has an empty path segment", expr)
		}
	}
	ref := &Ref{}
	switch head {
	case "params":
		if len(rest) == 0 {
			return nil, resolveErrf(path, "template reference %q must name a parameter", expr)
		}
		if _, ok := lex.params[rest[0]]; !ok {
			return nil, resolveErrf(path, "template reference %q names an undeclared param %q", expr, rest[0])
		}
		ref.Root = RootParams
		ref.Name = rest[0]
		ref.Path = rest[1:]
		return ref, nil
	case "steps":
		if len(rest) < 2 {
			return nil, resolveErrf(path, "template reference %q must be steps.<id>.output or steps.<id>.collect", expr)
		}
		info, ok := lex.steps[rest[0]]
		if !ok {
			return nil, resolveErrf(path, "template reference %q names an out-of-scope step %q", expr, rest[0])
		}
		field := rest[1]
		if field != "output" && field != "collect" {
			return nil, resolveErrf(path, "template reference %q must use output or collect, not %q", expr, field)
		}
		if field == "collect" && !info.hasCollect {
			return nil, resolveErrf(path, "template reference %q: step %q is a %s and has no collect", expr, rest[0], info.kind)
		}
		ref.Root = RootSteps
		ref.StepID = rest[0]
		ref.Field = field
		ref.Path = rest[2:]
		return ref, nil
	case "index":
		if len(rest) > 0 {
			return nil, resolveErrf(path, "template reference %q: index has no fields", expr)
		}
		if len(lex.items) == 0 {
			return nil, resolveErrf(path, "template reference %q: index is only in scope inside a fan-out or loop body", expr)
		}
		ref.Root = RootIndex
		return ref, nil
	case "run":
		if len(rest) == 0 || (rest[0] != "id" && rest[0] != "attempt") {
			return nil, resolveErrf(path, "template reference %q must be run.id or run.attempt", expr)
		}
		if len(rest) > 1 {
			return nil, resolveErrf(path, "template reference %q: run fields have no deeper path", expr)
		}
		ref.Root = RootRun
		ref.Field = rest[0]
		return ref, nil
	}
	// An item binding: the as-name itself, or the alias "item" for the
	// innermost binding.
	name := head
	if head == "item" && len(lex.items) > 0 {
		name = lex.items[len(lex.items)-1]
	}
	for _, binding := range lex.items {
		if binding == name {
			ref.Root = RootItem
			ref.Name = name
			ref.Path = rest
			return ref, nil
		}
	}
	if head == "item" {
		return nil, resolveErrf(path, "template reference %q: no item binding in scope", expr)
	}
	return nil, resolveErrf(path, "template reference %q: %q is not a step in scope, an item binding, or a known root", expr, head)
}

// Scope resolves compiled references at run time. The scheduler implements
// it over run state; the graph never holds run state.
type Scope interface {
	// Param resolves a declared workflow parameter.
	Param(name string) (any, bool)
	// StepField resolves a step's output or collect value.
	StepField(id string, field string) (any, error)
	// Item resolves a fan-out item by its as-name.
	Item(name string) (any, bool)
	// Index resolves the current iteration index.
	Index() int
	// RunField resolves run.id or run.attempt.
	RunField(field string) (any, error)
}

// Render options. Values above HandleThreshold render as a bounded preview
// and are recorded as handles the node can dereference via value_ref
// (§5.4: large values are references, not text).
type RenderOptions struct {
	HandleThreshold int
	PreviewBytes    int
}

// Defaults for zero RenderOptions.
const (
	DefaultHandleThreshold = 8 << 10 // 8 KiB
	DefaultPreviewBytes    = 512
)

func (o RenderOptions) threshold() int {
	if o.HandleThreshold <= 0 {
		return DefaultHandleThreshold
	}
	return o.HandleThreshold
}

func (o RenderOptions) preview() int {
	if o.PreviewBytes <= 0 {
		return DefaultPreviewBytes
	}
	return o.PreviewBytes
}

// Handle is a value that exceeded the handle threshold: it renders as a
// bounded preview, and Ref says where the full value lives.
type Handle struct {
	Ref     *Ref   `json:"ref"`
	Preview string `json:"preview"`
	Size    int    `json:"size"`
}

// RenderResult is a rendered template: the text plus any oversized values
// bound as handles.
type RenderResult struct {
	Text    string   `json:"text"`
	Handles []Handle `json:"handles,omitempty"`
}

// Render walks the compiled segments against the scope. Reference strings
// are never re-parsed here.
func (t *Template) Render(s Scope, opts RenderOptions) (RenderResult, error) {
	if t == nil {
		return RenderResult{}, nil
	}
	var b strings.Builder
	var handles []Handle
	for _, seg := range t.Segments {
		if seg.Ref == nil {
			b.WriteString(seg.Literal)
			continue
		}
		v, err := resolveRefValue(seg.Ref, s)
		if err != nil {
			return RenderResult{}, err
		}
		text, err := valueText(v)
		if err != nil {
			return RenderResult{}, &RenderError{Ref: seg.Ref.String(), Message: err.Error()}
		}
		if len(text) > opts.threshold() {
			preview := text
			if len(preview) > opts.preview() {
				preview = preview[:opts.preview()]
			}
			handles = append(handles, Handle{Ref: seg.Ref, Preview: preview, Size: len(text)})
			b.WriteString(preview)
			continue
		}
		b.WriteString(text)
	}
	return RenderResult{Text: b.String(), Handles: handles}, nil
}

// RenderError locates a render failure at a compiled reference.
type RenderError struct {
	Ref     string
	Message string
}

func (e *RenderError) Error() string {
	return e.Ref + ": " + e.Message
}

func resolveRefValue(r *Ref, s Scope) (any, error) {
	switch r.Root {
	case RootParams:
		v, ok := s.Param(r.Name)
		if !ok {
			return nil, &RenderError{Ref: r.String(), Message: fmt.Sprintf("param %q is unbound", r.Name)}
		}
		return walkPath(v, r.Path, r)
	case RootSteps:
		v, err := s.StepField(r.StepID, r.Field)
		if err != nil {
			return nil, &RenderError{Ref: r.String(), Message: err.Error()}
		}
		return walkPath(v, r.Path, r)
	case RootItem:
		v, ok := s.Item(r.Name)
		if !ok {
			return nil, &RenderError{Ref: r.String(), Message: fmt.Sprintf("item %q is unbound", r.Name)}
		}
		return walkPath(v, r.Path, r)
	case RootIndex:
		return s.Index(), nil
	case RootRun:
		return s.RunField(r.Field)
	}
	return nil, &RenderError{Ref: r.String(), Message: "unknown root"}
}

func walkPath(v any, path []string, r *Ref) (any, error) {
	for _, seg := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, &RenderError{Ref: r.String(), Message: fmt.Sprintf("value has no field %q (value is not a mapping)", seg)}
		}
		v, ok = m[seg]
		if !ok {
			return nil, &RenderError{Ref: r.String(), Message: fmt.Sprintf("value has no field %q", seg)}
		}
	}
	return v, nil
}

// valueText renders a resolved value as template text. Scalars render
// directly; composites render as compact JSON.
func valueText(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case int:
		return strconv.Itoa(t), nil
	case int32:
		return strconv.FormatInt(int64(t), 10), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	case json.Number:
		return t.String(), nil
	default:
		enc, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(enc), nil
	}
}
