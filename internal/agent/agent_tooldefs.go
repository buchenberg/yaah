package agent

import (
	"encoding/json"

	"github.com/buchenberg/yaah/internal/types"
)

// buildToolDefs builds the OpenAI-format tool definitions from the registry.
// The result is cached and reused across loop iterations until the registry's
// generation changes (i.e. a tool is registered), so the per-turn cost drops
// from a full schema re-read and re-allocation to a single integer compare.
func (l *Loop) buildToolDefs() []types.ToolDef {
	if l.Registry == nil {
		return nil
	}
	gen := l.Registry.Generation()
	if l.toolDefsCache != nil && l.toolDefsGen == gen {
		return l.toolDefsCache
	}
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
	l.toolDefsCache = toolDefs
	l.toolDefsGen = gen
	return toolDefs
}
