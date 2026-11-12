package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// DecodeError locates a document decode failure. Path is the document
// location ("spec.steps[1].body"), empty at the document root.
type DecodeError struct {
	Path    string
	Message string
}

func (e *DecodeError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

func decodeErrf(path, format string, args ...any) *DecodeError {
	return &DecodeError{Path: path, Message: fmt.Sprintf(format, args...)}
}

// Parse decodes a workflow document from YAML or JSON bytes. Documents
// with a leading '{' take the JSON path; everything else is YAML. Both
// produce identical Document values for equivalent inputs.
func Parse(data []byte) (*Document, error) {
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF")), " \t\r\n")
	if bytes.HasPrefix(trimmed, []byte("{")) {
		return ParseJSON(data)
	}
	return ParseYAML(data)
}

// ParseYAML decodes a workflow document from YAML bytes.
func ParseYAML(data []byte) (*Document, error) {
	return decodeDocument(data, false)
}

// ParseJSON decodes a workflow document from JSON bytes.
func ParseJSON(data []byte) (*Document, error) {
	return decodeDocument(data, true)
}

func decodeDocument(data []byte, jsonFormat bool) (*Document, error) {
	if len(data) > MaxSpecBytes {
		return nil, decodeErrf("", "spec is %d bytes (limit %d bytes)", len(data), MaxSpecBytes)
	}
	tree, err := parseTree(data, jsonFormat)
	if err != nil {
		return nil, err
	}

	var doc Document
	if err := decodeInto("", tree, reflect.ValueOf(&doc).Elem()); err != nil {
		return nil, err
	}
	// Top-level steps must be named; body nodes are positional and need
	// no id.
	for i := range doc.Spec.Steps {
		if doc.Spec.Steps[i].ID == "" {
			return nil, decodeErrf(fmt.Sprintf("spec.steps[%d].id", i), "missing required field %q", "id")
		}
	}
	if err := checkLimits(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func parseTree(data []byte, jsonFormat bool) (any, error) {
	if jsonFormat {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var tree any
		if err := dec.Decode(&tree); err != nil {
			return nil, decodeErrf("", "invalid JSON: %v", err)
		}
		if dec.More() {
			return nil, decodeErrf("", "invalid JSON: trailing data after the document")
		}
		return normalizeTree(tree)
	}
	var tree any
	if err := yaml.Unmarshal(data, &tree); err != nil {
		return nil, decodeErrf("", "invalid YAML: %v", err)
	}
	return normalizeTree(tree)
}

// normalizeTree converts YAML's map[any]any form into map[string]any and
// normalizes scalars so the JSON and YAML paths feed the walker identical
// trees. YAML timestamps are rejected: the model has no timestamp values,
// and an unquoted date would silently diverge from its JSON twin.
func normalizeTree(v any) (any, error) {
	switch t := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprint(k)
			}
			nv, err := normalizeTree(val)
			if err != nil {
				return nil, err
			}
			out[ks] = nv
		}
		return out, nil
	case map[string]any:
		for k, val := range t {
			nv, err := normalizeTree(val)
			if err != nil {
				return nil, err
			}
			t[k] = nv
		}
		return t, nil
	case []any:
		for i := range t {
			nv, err := normalizeTree(t[i])
			if err != nil {
				return nil, err
			}
			t[i] = nv
		}
		return t, nil
	case time.Time:
		return nil, decodeErrf("", "YAML timestamp values are not supported; quote the value as a string")
	default:
		return v, nil
	}
}

// normalizeScalar coerces tree scalars into parser-independent forms:
// integral values become int64 and fractional floats become float64, so
// YAML- and JSON-parsed documents compare deep-equal.
func normalizeScalar(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case float32:
		return float64(t)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = normalizeScalar(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			out[k] = normalizeScalar(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			if ks, ok := k.(string); ok {
				out[ks] = normalizeScalar(item)
				continue
			}
			out[fmt.Sprint(k)] = normalizeScalar(item)
		}
		return out
	default:
		return v
	}
}

// shorthand is implemented by fields that accept a bare-string form
// alongside their full mapping form.
type shorthand interface {
	decodeShorthand(path string, v any) error
}

var shorthandType = reflect.TypeOf((*shorthand)(nil)).Elem()

func decodeInto(path string, v any, target reflect.Value) error {
	if v == nil {
		return nil // explicit null leaves the target zeroed
	}
	if target.CanAddr() {
		if sh, ok := target.Addr().Interface().(shorthand); ok {
			return sh.decodeShorthand(path, v)
		}
	}
	if target.Kind() == reflect.Ptr && target.Type().Implements(shorthandType) {
		if target.IsNil() {
			target.Set(reflect.New(target.Type().Elem()))
		}
		return target.Interface().(shorthand).decodeShorthand(path, v)
	}
	switch target.Kind() {
	case reflect.Struct:
		m, err := stringKeyMap(path, v)
		if err != nil {
			return err
		}
		return decodeObject(path, m, target)
	case reflect.Slice:
		items, ok := v.([]any)
		if !ok {
			return decodeErrf(path, "expected a list, got %s", valueKind(v))
		}
		out := reflect.MakeSlice(target.Type(), len(items), len(items))
		for i, item := range items {
			// Decode through a pointer buffer so shorthand element
			// types (Selector, Node inside Body) work uniformly.
			ev := reflect.New(target.Type().Elem()).Elem()
			if err := decodeInto(fmt.Sprintf("%s[%d]", path, i), item, ev); err != nil {
				return err
			}
			out.Index(i).Set(ev)
		}
		target.Set(out)
		return nil
	case reflect.Map:
		m, err := stringKeyMap(path, v)
		if err != nil {
			return err
		}
		if target.IsNil() {
			target.Set(reflect.MakeMap(target.Type()))
		}
		elemType := target.Type().Elem()
		for _, k := range sortedKeys(m) {
			ev := reflect.New(elemType).Elem()
			if err := decodeInto(joinField(path, k), m[k], ev); err != nil {
				return err
			}
			target.SetMapIndex(reflect.ValueOf(k).Convert(target.Type().Key()), ev)
		}
		return nil
	case reflect.Ptr:
		ev := reflect.New(target.Type().Elem())
		if err := decodeInto(path, v, ev.Elem()); err != nil {
			return err
		}
		target.Set(ev)
		return nil
	case reflect.Interface:
		target.Set(reflect.ValueOf(normalizeScalar(v)))
		return nil
	case reflect.String:
		s, ok := v.(string)
		if !ok {
			return decodeErrf(path, "expected a string, got %s", valueKind(v))
		}
		if err := checkEnum(target.Type(), s, path); err != nil {
			return err
		}
		target.SetString(s)
		return nil
	case reflect.Bool:
		b, ok := v.(bool)
		if !ok {
			return decodeErrf(path, "expected a boolean, got %s", valueKind(v))
		}
		target.SetBool(b)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := intValue(path, v)
		if err != nil {
			return err
		}
		if target.OverflowInt(n) {
			return decodeErrf(path, "integer %d out of range", n)
		}
		target.SetInt(n)
		return nil
	case reflect.Float32, reflect.Float64:
		f, err := floatValue(path, v)
		if err != nil {
			return err
		}
		target.SetFloat(f)
		return nil
	default:
		return decodeErrf(path, "unsupported target type %s", target.Type())
	}
}

func decodeObject(path string, m map[string]any, target reflect.Value) error {
	info := infoFor(target.Type())
	// Peek at a node's kind before walking fields, so a bad kind is
	// reported as an unknown kind rather than as a side effect of an
	// unrelated field error.
	if target.Type() == nodeType {
		if raw, present := m["kind"]; present {
			if s, ok := raw.(string); ok && !nodeKinds[s] {
				return decodeErrf(joinField(path, "kind"), "unknown node kind %q", s)
			}
		}
	}
	for _, key := range sortedKeys(m) {
		idx, known := info.fields[key]
		if !known {
			return decodeErrf(joinField(path, key), "unknown field %q", key)
		}
		if m[key] == nil {
			continue
		}
		if err := decodeInto(joinField(path, key), m[key], target.Field(idx)); err != nil {
			return err
		}
	}
	// Required fields are those without omitempty: the tag set doubles as
	// the schema, so requiredness cannot drift between decoder and schema.
	// An explicit null counts as missing.
	for _, tag := range info.ordered {
		if info.required[tag] {
			v, present := m[tag]
			if !present || v == nil {
				return decodeErrf(joinField(path, tag), "missing required field %q", tag)
			}
		}
	}
	return nil
}

func (s *Selector) decodeShorthand(path string, v any) error {
	if str, ok := v.(string); ok {
		if str == "" {
			return decodeErrf(path, "selector path must not be empty")
		}
		s.From = str
		return nil
	}
	m, err := stringKeyMap(path, v)
	if err != nil {
		return decodeErrf(path, "expected a selector path or mapping, got %s", valueKind(v))
	}
	return decodeObject(path, m, reflect.ValueOf(s).Elem())
}

func (o *OutputSpec) decodeShorthand(path string, v any) error {
	if str, ok := v.(string); ok {
		if str == "" {
			return decodeErrf(path, "output name must not be empty")
		}
		o.Name = str
		return nil
	}
	m, err := stringKeyMap(path, v)
	if err != nil {
		return decodeErrf(path, "expected an output name or mapping, got %s", valueKind(v))
	}
	return decodeObject(path, m, reflect.ValueOf(o).Elem())
}

func (o *Over) decodeShorthand(path string, v any) error {
	switch t := v.(type) {
	case string:
		if t == "" {
			return decodeErrf(path, "selector path must not be empty")
		}
		o.Selector = &Selector{From: t}
		return nil
	case []any:
		o.Values = make([]any, len(t))
		for i, item := range t {
			o.Values[i] = normalizeScalar(item)
		}
		return nil
	}
	if _, ok := v.(map[string]any); !ok {
		if _, ok := v.(map[any]any); !ok {
			return decodeErrf(path, "expected a selector path, mapping, or literal list, got %s", valueKind(v))
		}
	}
	m, _ := stringKeyMap(path, v)
	o.Selector = &Selector{}
	return decodeObject(path, m, reflect.ValueOf(o.Selector).Elem())
}

func (b *Body) decodeShorthand(path string, v any) error {
	switch t := v.(type) {
	case map[string]any, map[any]any:
		var n Node
		m, err := stringKeyMap(path, v)
		if err != nil {
			return err
		}
		if err := decodeObject(path, m, reflect.ValueOf(&n).Elem()); err != nil {
			return err
		}
		*b = Body{n}
		return nil
	case []any:
		out := make(Body, 0, len(t))
		for i, item := range t {
			m, err := stringKeyMap(fmt.Sprintf("%s[%d]", path, i), item)
			if err != nil {
				return err
			}
			var n Node
			if err := decodeObject(fmt.Sprintf("%s[%d]", path, i), m, reflect.ValueOf(&n).Elem()); err != nil {
				return err
			}
			out = append(out, n)
		}
		*b = out
		return nil
	default:
		return decodeErrf(path, "expected a node or a list of nodes, got %s", valueKind(v))
	}
}

// structInfo is the reflection-derived field table shared by the decoder
// and the JSON Schema emitter.
type structInfo struct {
	fields   map[string]int
	ordered  []string
	required map[string]bool
}

var structCache sync.Map // reflect.Type -> *structInfo

func infoFor(t reflect.Type) *structInfo {
	if cached, ok := structCache.Load(t); ok {
		return cached.(*structInfo)
	}
	info := &structInfo{
		fields:   make(map[string]int),
		required: make(map[string]bool),
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		tag, ok := f.Tag.Lookup("json")
		if !ok || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		if _, dup := info.fields[name]; dup {
			panic("spec: duplicate json tag " + name + " on " + t.Name())
		}
		info.fields[name] = i
		info.ordered = append(info.ordered, name)
		if !strings.Contains(tag, "omitempty") {
			info.required[name] = true
		}
	}
	structCache.Store(t, info)
	return info
}

func stringKeyMap(path string, v any) (map[string]any, error) {
	switch t := v.(type) {
	case map[string]any:
		return t, nil
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			ks, ok := k.(string)
			if !ok {
				return nil, decodeErrf(path, "mapping keys must be strings, got %v", k)
			}
			out[ks] = val
		}
		return out, nil
	default:
		return nil, decodeErrf(path, "expected a mapping, got %s", valueKind(v))
	}
}

func intValue(path string, v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, nil
		}
		if f, err := n.Float64(); err == nil && f == math.Trunc(f) {
			return int64(f), nil
		}
		return 0, decodeErrf(path, "expected an integer, got %s", n.String())
	case float64:
		if n == math.Trunc(n) {
			return int64(n), nil
		}
	}
	return 0, decodeErrf(path, "expected an integer, got %s", valueKind(v))
}

func floatValue(path string, v any) (float64, error) {
	switch n := v.(type) {
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, nil
		}
		return 0, decodeErrf(path, "expected a number, got %s", n.String())
	case float64:
		return n, nil
	}
	return 0, decodeErrf(path, "expected a number, got %s", valueKind(v))
}

func valueKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case int, int32, int64, json.Number, float32, float64:
		return "number"
	case []any:
		return "list"
	case map[string]any, map[any]any:
		return "mapping"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func joinField(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// checkLimits enforces the parse limits on a decoded document: node
// count, prompt sizes, and template depth. All errors carry the path of
// the offending node or field.
func checkLimits(doc *Document) error {
	nodes := 0
	return walkValue(reflect.ValueOf(doc).Elem(), "", func(path string, v reflect.Value) error {
		switch {
		case v.Type() == nodeType:
			nodes++
			if nodes > MaxNodes {
				return decodeErrf(path, "too many nodes: limit is %d", MaxNodes)
			}
			if err := checkPrompt(joinField(path, "prompt"), v.FieldByName("Prompt")); err != nil {
				return err
			}
		case v.Type() == judgeType:
			if err := checkPrompt(joinField(path, "prompt"), v.FieldByName("Prompt")); err != nil {
				return err
			}
		}
		if v.Kind() == reflect.String {
			if depth := templateDepth(v.String()); depth > MaxTemplateDepth {
				return decodeErrf(path, "template nesting depth %d exceeds limit %d", depth, MaxTemplateDepth)
			}
		}
		return nil
	})
}

func checkPrompt(path string, v reflect.Value) error {
	s := v.String()
	if s == "" {
		return nil
	}
	if len(s) > MaxPromptBytes {
		return decodeErrf(path, "prompt is %d bytes (limit %d bytes)", len(s), MaxPromptBytes)
	}
	return nil
}

// templateDepth reports the deepest {{ ... }} nesting in s.
func templateDepth(s string) int {
	depth, max := 0, 0
	for i := 0; i < len(s)-1; i++ {
		switch {
		case s[i] == '{' && s[i+1] == '{':
			depth++
			if depth > max {
				max = depth
			}
			i++
		case s[i] == '}' && s[i+1] == '}':
			if depth > 0 {
				depth--
			}
			i++
		}
	}
	return max
}

// walkValue visits every value in v with its document path, descending
// structs (exported json-tagged fields), slices, maps, pointers, and
// interfaces.
func walkValue(v reflect.Value, path string, visit func(path string, v reflect.Value) error) error {
	if err := visit(path, v); err != nil {
		return err
	}
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return walkValue(v.Elem(), path, visit)
	case reflect.Struct:
		info := infoFor(v.Type())
		for _, tag := range info.ordered {
			if err := walkValue(v.Field(info.fields[tag]), joinField(path, tag), visit); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := walkValue(v.Index(i), fmt.Sprintf("%s[%d]", path, i), visit); err != nil {
				return err
			}
		}
	case reflect.Map:
		keys := v.MapKeys()
		strs := make([]string, 0, len(keys))
		byKey := make(map[string]reflect.Value, len(keys))
		for _, k := range keys {
			s := k.String()
			strs = append(strs, s)
			byKey[s] = k
		}
		sort.Strings(strs)
		for _, s := range strs {
			if err := walkValue(v.MapIndex(byKey[s]), joinField(path, s), visit); err != nil {
				return err
			}
		}
	}
	return nil
}
