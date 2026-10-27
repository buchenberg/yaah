package tools

// schemas_test.go: every tool's Schema() must be valid JSON. The schemas are
// json.RawMessage literals embedded in the request payload, so an invalid one
// fails every API call that includes the tool — not just calls to that tool —
// and a test is the only thing between a typo and a session that cannot start.

import (
	"encoding/json"
	"testing"
)

func TestAllToolSchemas_AreValidJSON(t *testing.T) {
	// allBuiltinTools covers the leaf registry and every runtime-wired
	// tool, including ones cmd/yaah registers conditionally.
	for _, tool := range allBuiltinTools() {
		if tool == nil {
			continue
		}
		t.Run(tool.Name(), func(t *testing.T) {
			var v any
			if err := json.Unmarshal(tool.Schema(), &v); err != nil {
				t.Errorf("schema for %s is not valid JSON: %v", tool.Name(), err)
			}
			if m, ok := v.(map[string]any); !ok || m["type"] != "object" {
				t.Errorf("%s: schema must be a JSON object with type=object", tool.Name())
			}
		})
	}
}
