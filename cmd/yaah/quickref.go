package yaah

import (
	"encoding/json"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/tools"
)

// buildToolQuickRef builds a compact tool reference string for the
// system prompt. It lists every tool name, signature, and description
// in a single block, mimicking the help text the user sees.
func buildToolQuickRef(toolReg *tools.Registry) string {
	names := toolReg.List()
	entries := make([]prompts.QuickRefEntry, 0, len(names))
	for _, name := range names {
		t := toolReg.Get(name)
		if t == nil {
			continue
		}
		sig := schemaSignature(t.Schema())
		entries = append(entries, prompts.QuickRefEntry{
			Name:        name,
			Signature:   sig,
			Description: t.Description(),
		})
	}
	return prompts.BuildToolQuickRef(entries)
}

// schemaSignature extracts a compact comma-separated parameter list
// from a JSON Schema object. Optional properties get a "?" suffix.
func schemaSignature(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	if len(s.Properties) == 0 {
		return ""
	}
	required := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		required[r] = true
	}
	params := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		if required[name] {
			params = append(params, name)
		} else {
			params = append(params, name+"?")
		}
	}
	return strings.Join(params, ", ")
}
