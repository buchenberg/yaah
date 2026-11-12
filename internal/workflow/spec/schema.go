package spec

import (
	"bytes"
	"reflect"
)

// JSONSchema returns the canonical JSON Schema (draft 2020-12) describing
// the v1 workflow document surface. It is generated from the same Go
// types and enum registry the decoder uses, so the schema cannot drift
// from what Parse accepts: struct fields become properties, non-omitempty
// fields become required, enum types become string enums, and shorthand
// fields become anyOf unions. The bytes are canonical (sorted keys,
// compact) and stable across calls.
func JSONSchema() ([]byte, error) {
	defs := map[string]any{}
	docRef := structSchema(documentType, defs)
	root := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id":     string(APIVersionV1),
		"title":   "yaah workflow document",
		"$ref":    docRef["$ref"],
		"$defs":   defs,
	}
	var buf bytes.Buffer
	if err := writeCanonicalValue(&buf, root); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// shorthandSchemas maps types with a bare-string (or list) shorthand to
// the anyOf branches that mirror the decoder's shorthand behavior. It is
// populated in init to avoid an initialization cycle with structSchema.
var shorthandSchemas = map[reflect.Type]func(defs map[string]any) []any{}

func init() {
	shorthandSchemas[selectorType] = func(defs map[string]any) []any {
		return []any{
			map[string]any{"type": "string"},
			structSchema(selectorType, defs),
		}
	}
	shorthandSchemas[outputSpType] = func(defs map[string]any) []any {
		return []any{
			map[string]any{"type": "string"},
			structSchema(outputSpType, defs),
		}
	}
	shorthandSchemas[overType] = func(defs map[string]any) []any {
		return []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "array", "items": map[string]any{}},
			structSchema(selectorType, defs),
		}
	}
	shorthandSchemas[bodyType] = func(defs map[string]any) []any {
		return []any{
			structSchema(nodeType, defs),
			map[string]any{"type": "array", "items": structSchema(nodeType, defs)},
		}
	}
}

func structSchema(t reflect.Type, defs map[string]any) map[string]any {
	name := t.Name()
	if _, ok := defs[name]; !ok {
		defs[name] = map[string]any{"$placeholder": true}
		info := infoFor(t)
		props := map[string]any{}
		var required []any
		for _, tag := range info.ordered {
			props[tag] = propertySchema(t, tag, t.Field(info.fields[tag]).Type, defs)
			if info.required[tag] {
				required = append(required, tag)
			}
		}
		schema := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           props,
		}
		if len(required) > 0 {
			schema["required"] = required
		}
		defs[name] = schema
	}
	return map[string]any{"$ref": "#/$defs/" + name}
}

// propertySchema wraps a field schema with position-specific rules the
// flat structs cannot express. Currently: top-level steps must carry an
// id, body nodes need not.
func propertySchema(owner reflect.Type, tag string, t reflect.Type, defs map[string]any) map[string]any {
	schema := fieldSchema(t, defs)
	if owner == specType && tag == "steps" {
		items := schema["items"]
		schema["items"] = map[string]any{
			"allOf": []any{
				items,
				map[string]any{"required": []any{"id"}},
			},
		}
	}
	return schema
}

func fieldSchema(t reflect.Type, defs map[string]any) map[string]any {
	if gen, ok := shorthandSchemas[t]; ok {
		return map[string]any{"anyOf": gen(defs)}
	}
	if values, ok := enumRegistry[t]; ok {
		ev := make([]any, len(values))
		for i, v := range values {
			ev[i] = v
		}
		return map[string]any{"type": "string", "enum": ev}
	}
	switch t.Kind() {
	case reflect.Ptr:
		return fieldSchema(t.Elem(), defs)
	case reflect.Struct:
		return structSchema(t, defs)
	case reflect.Slice:
		return map[string]any{"type": "array", "items": fieldSchema(t.Elem(), defs)}
	case reflect.Map:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": fieldSchema(t.Elem(), defs),
		}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Interface:
		return map[string]any{}
	}
	return map[string]any{}
}
