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
	// Leaf tools from the built-in registry, plus the tools the wiring
	// registers directly.
	all := []Tool{
		&ApplyOutTool{},
		&BackgroundProcessTool{},
		&ListSubAgentsTool{},
		&MemoryAddTool{},
		&MemoryDeleteTool{},
		&MemorySearchTool{},
		&MemorySessionSearchTool{},
		&MemoryUpdateTool{},
		&PlanTool{},
		&ProposalTool{},
		&RoleTool{},
		&SubAgentJobsTool{},
		&SupervisorTool{},
		&DelegateTool{},
		&TodoWriteTool{},
	}
	// NewLeafTool reaches every leaf tool, including the shell this platform
	// does not register (NewRegistry omits it, which would silently skip a
	// schema).
	for name := range leafTools {
		all = append(all, NewLeafTool(name))
	}

	for _, tool := range all {
		if tool == nil {
			continue
		}
		var v any
		if err := json.Unmarshal(tool.Schema(), &v); err != nil {
			t.Errorf("%s: schema is not valid JSON: %v", tool.Name(), err)
			continue
		}
		if m, ok := v.(map[string]any); !ok || m["type"] != "object" {
			t.Errorf("%s: schema must be a JSON object with type=object", tool.Name())
		}
	}
}
