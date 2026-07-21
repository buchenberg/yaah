package agent

import (
	"encoding/json"

	"github.com/buchenberg/yaah/internal/types"
)

// buildToolDefs builds the OpenAI-format tool definitions from the registry.
func (l *Loop) buildToolDefs() []types.ToolDef {
	toolNames := l.Registry.List()
	toolDefs := make([]types.ToolDef, 0, len(toolNames))
	for _, name := range toolNames {
		t := l.Registry.Get(name)
		if t == nil {
			continue
		}
		toolDefs = append(toolDefs, types.ToolDef{
			Type: "function",
			Function: types.ToolFn{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  json.RawMessage(t.Schema()),
			},
		})
	}
	return toolDefs
}
