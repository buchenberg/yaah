package toolargs

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// UnknownArgumentError reports a tool call that named a key the tool does not
// accept. The model sees the allowed list so the next attempt can be right,
// instead of the typo silently changing behavior (the reason this exists:
// "backgroud": true ran a foreground dispatch).
type UnknownArgumentError struct {
	Tool      string
	Arguments []string
	Allowed   []string
}

func (e *UnknownArgumentError) Error() string {
	return fmt.Sprintf("%s: unknown argument(s) %s; allowed: %s",
		e.Tool, quoteList(e.Arguments), strings.Join(e.Allowed, ", "))
}

// DecodeStrictObject decodes a JSON object and rejects every key outside
// allowed. Tools that need numeric-string coercion decode the returned map,
// coerce, then unmarshal into their typed params — the strict check happens
// before any behavior is chosen.
func DecodeStrictObject(tool, args string, allowed ...string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return nil, fmt.Errorf("%s: invalid arguments: %w", tool, err)
	}
	permitted := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		permitted[key] = true
	}
	var unknown []string
	for key := range raw {
		if !permitted[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		sorted := append([]string(nil), allowed...)
		sort.Strings(sorted)
		return nil, &UnknownArgumentError{Tool: tool, Arguments: unknown, Allowed: sorted}
	}
	return raw, nil
}

func quoteList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}
	return strings.Join(quoted, ", ")
}

// StrictToolArgs decodes args into out using the tool's own schema as the
// source of truth for the allowed keys: every top-level property is accepted,
// every other key is rejected with the allowed list. Tools with no arguments
// may pass an empty string.
func StrictToolArgs(tool string, schema json.RawMessage, args string, out any) error {
	keys, err := SchemaPropertyNames(schema)
	if err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	raw, err := DecodeStrictObject(tool, args, keys...)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("%s: invalid arguments: %w", tool, err)
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%s: invalid arguments: %w", tool, err)
	}
	return nil
}

// SchemaPropertyNames returns the sorted top-level property names of a tool
// schema. An empty set is legitimate — a no-argument tool such as
// list_subagents has `"properties": {}` — and means every supplied key is
// unknown.
func SchemaPropertyNames(schema json.RawMessage) ([]string, error) {
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return nil, fmt.Errorf("tool schema is not JSON: %w", err)
	}
	names := make([]string, 0, len(parsed.Properties))
	for name := range parsed.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// CloseSchema returns the schema with additionalProperties:false so the model
// is told unknown keys are not accepted. An explicit value is preserved (an
// MCP tool or a free-form schema may deliberately allow extras). Malformed
// input is returned unchanged: the schema tests catch it, and a decode error
// must not silently reshape a tool contract at request time.
func CloseSchema(schema json.RawMessage) json.RawMessage {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(schema, &doc); err != nil {
		return schema
	}
	if _, ok := doc["additionalProperties"]; ok {
		return schema
	}
	doc["additionalProperties"] = json.RawMessage("false")
	out, err := json.Marshal(doc)
	if err != nil {
		return schema
	}
	return out
}
