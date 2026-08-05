package tools

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func (t *TaskTool) Schema() json.RawMessage {
	known := t.roleNames()
	if len(known) > 0 {
		return BuildTaskSchema(known, t.RoleDescriptions)
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"description": {"type": "string", "description": "3-5 word description of the subtask"},
			"prompt": {"type": "string", "description": "The task for the sub-agent to perform autonomously"},
			"role": {"type": "string", "description": "Sub-agent role selecting its tool set and limits. Required. Use list_subagents to see available roles."},
			"max_iterations": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Optional cap on sub-agent loop turns. Overrides the role default."},
			"max_turns": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Optional soft cap on tool-using turns. Overrides the role default."},
			"json_mode": {"type": "boolean", "description": "Request structured JSON output from the sub-agent."},
			"output_limit": {"type": "integer", "minimum": 1024, "description": "Optional byte cap on the sub-agent's final report."},
			"background": {"type": "boolean", "description": "When true, dispatch the sub-agent asynchronously and return immediately. Results arrive in a follow-up message. Returns a job_id you can use with subagent_jobs to check status, cancel, or wait."}
		},
		"required": ["description", "prompt", "role"]
	}`)
}

// roleNames returns the known role names for validation and schema
// generation: the cached startup snapshot layered with the live resolver
// (if any) so role create/delete via the role tool takes immediate effect.
func (t *TaskTool) roleNames() []string {
	// Build the set of known roles: start with the cached snapshot,
	// then layer on the live resolver so role create/delete takes
	// immediate effect.
	known := make(map[string]bool)
	for _, n := range t.RoleNames {
		known[n] = true
	}
	if t.RoleResolver != nil {
		for _, n := range t.RoleResolver() {
			known[n] = true
		}
	}
	names := make([]string, 0, len(known))
	for n := range known {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// BuildTaskSchema returns a JSON Schema for the task tool whose role
// enum is populated from the given list of role names, so user-defined
// roles discovered at runtime are visible to the model.
func BuildTaskSchema(roleNames []string, roleDescriptions map[string]string) json.RawMessage {
	roles := make([]string, len(roleNames))
	copy(roles, roleNames)

	roleDesc := "Sub-agent role selecting its tool set and limits."
	if len(roleDescriptions) > 0 {
		var b strings.Builder
		b.WriteString("Sub-agent role. Available:\n")
		for _, name := range roles {
			if d, ok := roleDescriptions[name]; ok && d != "" {
				fmt.Fprintf(&b, "- %s: %s\n", name, d)
			} else {
				fmt.Fprintf(&b, "- %s\n", name)
			}
		}
		b.WriteString("Required — use list_subagents for full details.")
		roleDesc = strings.TrimSpace(b.String())
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{
				"type":        "string",
				"description": "3-5 word description of the subtask",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the sub-agent to perform autonomously",
			},
			"role": map[string]any{
				"type":        "string",
				"enum":        roles,
				"description": roleDesc,
			},
			"max_iterations": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     50,
				"description": "Optional cap on sub-agent loop turns. Overrides the role default.",
			},
			"max_turns": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     50,
				"description": "Optional soft cap on tool-using turns. Overrides the role default.",
			},
			"json_mode": map[string]any{
				"type":        "boolean",
				"description": "Request structured JSON output from the sub-agent.",
			},
			"output_limit": map[string]any{
				"type":        "integer",
				"minimum":     1024,
				"description": "Optional byte cap on the sub-agent's final report.",
			},
			"background": map[string]any{
				"type":        "boolean",
				"description": "When true, dispatch the sub-agent asynchronously and return immediately. Results arrive in a follow-up message. Returns a job_id to use with subagent_jobs (status/cancel/wait).",
			},
		},
		"required": []string{"description", "prompt", "role"},
	}
	data, _ := json.Marshal(schema)
	return json.RawMessage(data)
}
