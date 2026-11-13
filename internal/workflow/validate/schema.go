package validate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// maxSchemaDepth bounds how deep an inline output schema may nest. The
// document itself is already capped by spec.MaxSpecBytes and spec.MaxNodes;
// this is the analogous guard for the one free-form string in the graph.
const maxSchemaDepth = 16

// schemaTypes are the JSON Schema primitive type names.
var schemaTypes = map[string]bool{
	"null": true, "boolean": true, "object": true, "array": true,
	"number": true, "string": true, "integer": true,
}

// schemaProblem reports why s is not a usable output schema, or "" when it
// is. OutputSpec.Schema is a string on the typed surface and the decoder
// checks nothing about its content, so both meaningful forms are accepted
// here and only here:
//
//	schema: report.v1             a name the schema registry resolves
//	schema: '{"type":"object"}'   an inline draft 2020-12 document
//
// The inline form gets structural sanity checks made with the standard
// library alone — this repo takes no JSON Schema validator dependency —
// aimed at what a hand-written schema gets wrong most often: the value must
// parse as one JSON object; `type` names JSON types; `required` is a list of
// property names; `enum` is a non-empty list; `properties`,
// `additionalProperties`, `items`, `not` and the three combinators receive
// schemas; and the nesting stays within maxSchemaDepth.
//
// Two forms are rejected on purpose. A top-level boolean schema (`true`) is
// valid JSON Schema but binds an output to no shape at all, so accepting it
// would only defer the confusion to the first consumer of the binding. And a
// nested boolean subschema is not rejected: it is legal inside `properties`,
// it is rare, and this rule is a typo net rather than a conformance suite.
func schemaProblem(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "output schema must not be blank"
	}
	if !strings.HasPrefix(trimmed, "{") {
		if isSchemaName(trimmed) {
			return ""
		}
		return fmt.Sprintf("output schema %q is neither a schema name nor a JSON object", truncated(trimmed, 60))
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return "output schema is not valid JSON: " + err.Error()
	}
	if dec.More() {
		return "output schema has trailing content after the JSON object"
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return "output schema must be a JSON object"
	}
	return schemaObjectProblem(obj, 0)
}

// schemaObjectProblem checks one schema object and descends into the
// keywords whose values are themselves schemas. The first failure is
// returned; a schema is either usable or the author needs one message.
func schemaObjectProblem(obj map[string]any, depth int) string {
	if depth > maxSchemaDepth {
		return fmt.Sprintf("output schema nests deeper than %d levels", maxSchemaDepth)
	}
	if v, ok := obj["type"]; ok {
		if msg := schemaTypeProblem(v); msg != "" {
			return msg
		}
	}
	if v, ok := obj["required"]; ok {
		list, ok := v.([]any)
		if !ok {
			return `output schema "required" must be a list of property names`
		}
		for i, item := range list {
			name, ok := item.(string)
			if !ok || name == "" {
				return fmt.Sprintf(`output schema "required[%d]" must be a non-empty property name`, i)
			}
		}
	}
	if v, ok := obj["enum"]; ok {
		list, ok := v.([]any)
		switch {
		case !ok:
			return `output schema "enum" must be a list of literals`
		case len(list) == 0:
			return `output schema "enum" is empty and can match no value`
		}
	}
	if v, ok := obj["properties"]; ok {
		props, ok := v.(map[string]any)
		if !ok {
			return `output schema "properties" must be an object of property schemas`
		}
		for _, name := range sortedKeys(props) {
			sub, ok := props[name].(map[string]any)
			if !ok {
				continue
			}
			if msg := schemaObjectProblem(sub, depth+1); msg != "" {
				return fmt.Sprintf("output schema property %q: %s", name, msg)
			}
		}
	}
	if v, ok := obj["additionalProperties"]; ok {
		if sub, isObject := v.(map[string]any); isObject {
			if msg := schemaObjectProblem(sub, depth+1); msg != "" {
				return msg
			}
		} else if _, isBool := v.(bool); !isBool {
			return `output schema "additionalProperties" must be a boolean or a schema`
		}
	}
	if v, ok := obj["items"]; ok {
		if list, isList := v.([]any); isList {
			for i, item := range list {
				sub, ok := item.(map[string]any)
				if !ok {
					return fmt.Sprintf("output schema items[%d] must be a schema object", i)
				}
				if msg := schemaObjectProblem(sub, depth+1); msg != "" {
					return fmt.Sprintf("output schema items[%d]: %s", i, msg)
				}
			}
		} else if sub, isObject := v.(map[string]any); isObject {
			if msg := schemaObjectProblem(sub, depth+1); msg != "" {
				return msg
			}
		} else {
			return `output schema "items" must be a schema or a list of schemas`
		}
	}
	if v, ok := obj["not"]; ok {
		sub, ok := v.(map[string]any)
		if !ok {
			return `output schema "not" must be a schema object`
		}
		if msg := schemaObjectProblem(sub, depth+1); msg != "" {
			return msg
		}
	}
	for _, kw := range [3]string{"allOf", "anyOf", "oneOf"} {
		v, ok := obj[kw]
		if !ok {
			continue
		}
		list, ok := v.([]any)
		switch {
		case !ok:
			return fmt.Sprintf("output schema %q must be a list of schemas", kw)
		case len(list) == 0:
			return fmt.Sprintf("output schema %q is empty and constrains nothing", kw)
		}
		for i, item := range list {
			sub, ok := item.(map[string]any)
			if !ok {
				return fmt.Sprintf("output schema %s[%d] must be a schema object", kw, i)
			}
			if msg := schemaObjectProblem(sub, depth+1); msg != "" {
				return fmt.Sprintf("output schema %s[%d]: %s", kw, i, msg)
			}
		}
	}
	return ""
}

// schemaTypeProblem accepts a single type name or a union list of distinct
// names, and nothing else.
func schemaTypeProblem(v any) string {
	if list, isList := v.([]any); isList {
		if len(list) == 0 {
			return `output schema "type" list is empty`
		}
		seen := map[string]bool{}
		for _, item := range list {
			name, ok := item.(string)
			if !ok {
				return `output schema "type" list must contain JSON type names`
			}
			if !schemaTypes[name] {
				return fmt.Sprintf("output schema type %q is not a JSON type", name)
			}
			if seen[name] {
				return fmt.Sprintf("output schema type %q is listed twice", name)
			}
			seen[name] = true
		}
		return ""
	}
	name, ok := v.(string)
	if !ok {
		return `output schema "type" must be a JSON type name or a list of them`
	}
	if !schemaTypes[name] {
		return fmt.Sprintf("output schema type %q is not a JSON type", name)
	}
	return ""
}

// isSchemaName reports whether s is a single schema-name token: ASCII
// letters, digits, and the separators a dotted or slash-delimited
// identifier uses, no whitespace, at most 128 bytes.
func isSchemaName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == ':' || c == '/':
		default:
			return false
		}
	}
	return true
}

// sortedKeys keeps schema messages deterministic: map iteration order would
// otherwise name whichever property Go happened to visit first.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// truncated bounds a value quoted into a message, on rune boundaries so a
// non-ASCII schema name cannot be cut mid-codepoint.
func truncated(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
