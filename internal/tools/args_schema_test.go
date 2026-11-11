package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestStrictToolArgs_UsesSchemaProperties(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"count":{"type":"integer"}}}`)
	var out struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	if err := StrictToolArgs("demo", schema, `{"name":"x","count":3}`, &out); err != nil {
		t.Fatalf("valid args: %v", err)
	}
	if out.Name != "x" || out.Count != 3 {
		t.Errorf("decoded = %+v, want x/3", out)
	}

	err := StrictToolArgs("demo", schema, `{"name":"x","nmae":"y"}`, &out)
	var unknown *UnknownArgumentError
	if !errors.As(err, &unknown) {
		t.Fatalf("unknown key = %v, want UnknownArgumentError", err)
	}
	if len(unknown.Allowed) != 2 || unknown.Allowed[0] != "count" || unknown.Allowed[1] != "name" {
		t.Errorf("allowed = %v, want [count name]", unknown.Allowed)
	}
	if !strings.Contains(err.Error(), "unknown argument") {
		t.Errorf("error should name the unknown argument: %v", err)
	}
}

func TestStrictToolArgs_EmptyArgsAreAnEmptyObject(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	var out struct {
		Name string `json:"name"`
	}
	if err := StrictToolArgs("demo", schema, "", &out); err != nil {
		t.Fatalf("empty args: %v", err)
	}
	if out.Name != "" {
		t.Errorf("empty args decoded %q, want zero value", out.Name)
	}
}

func TestStrictToolArgs_NoPropertySchemaAcceptsOnlyEmptyArgs(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{}}`)
	if err := StrictToolArgs("demo", schema, `{}`, &struct{}{}); err != nil {
		t.Fatalf("empty args: %v", err)
	}
	if err := StrictToolArgs("demo", schema, `{"anything":1}`, &struct{}{}); err == nil {
		t.Fatal("a no-argument tool must reject a supplied key")
	}
}

func TestCloseSchema(t *testing.T) {
	closed := CloseSchema(json.RawMessage(`{"type":"object","properties":{"a":{}}}`))
	var doc map[string]any
	if err := json.Unmarshal(closed, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", doc["additionalProperties"])
	}

	explicit := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	if got := CloseSchema(explicit); string(got) != string(explicit) {
		t.Errorf("an explicit value must be preserved, got %s", got)
	}

	malformed := json.RawMessage(`{not json`)
	if got := CloseSchema(malformed); string(got) != string(malformed) {
		t.Errorf("malformed schema must pass through, got %s", got)
	}
}

// TestAllBuiltinSchemasClosedAndDescribed is the coverage gate for was9.4: every
// built-in tool must advertise a closed object schema with named properties, so
// the strict decoder has a source of truth and the model is told what is
// accepted.
func TestAllBuiltinSchemasClosedAndDescribed(t *testing.T) {
	for _, tool := range allBuiltinTools() {
		name := tool.Name()
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(CloseSchema(tool.Schema()), &doc); err != nil {
			t.Errorf("%s: schema is not a JSON object: %v", name, err)
			continue
		}
		if string(doc["additionalProperties"]) != "false" {
			t.Errorf("%s: additionalProperties = %s, want false", name, doc["additionalProperties"])
		}
		rawProps, ok := doc["properties"]
		if !ok {
			t.Errorf("%s: schema has no top-level properties key", name)
			continue
		}
		var props map[string]json.RawMessage
		if err := json.Unmarshal(rawProps, &props); err != nil {
			t.Errorf("%s: properties is not an object: %v", name, err)
		}
	}
}

func TestDestructiveToolsRejectUnknownArguments(t *testing.T) {
	tools := []Tool{
		&ReplaceTool{},
		&BulkFilesTool{},
		NewGoModTool(),
		&GitTool{},
		&ApplyOutTool{},
	}
	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			_, err := tool.Execute(t.Context(), `{"typo":true}`)
			var unknown *UnknownArgumentError
			if !errors.As(err, &unknown) {
				t.Fatalf("Execute with an unknown key = %v, want UnknownArgumentError", err)
			}
			if len(unknown.Allowed) == 0 {
				t.Error("error must list the allowed keys")
			}
		})
	}
}
