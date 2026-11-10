package tools

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

func (t *DelegateTool) Schema() json.RawMessage {
	known := t.roleNames()
	if len(known) > 0 {
		return BuildDelegateSchema(known, t.RoleDescriptions)
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"description": {"type": "string", "description": "3-5 word description of the subtask"},
			"prompt": {"type": "string", "description": "The task for the sub-agent to perform autonomously"},
			"role": {"type": "string", "description": "Sub-agent role selecting its tool set and limits. Required. Use list_subagents to see available roles."},
			"max_iterations": {"type": "integer", "minimum": 1, "maximum": 100, "description": "Optional iteration cap. Prefer omitting — roles carry tuned budgets, and the value cannot go below the role's min_iterations."},
			"timeout_seconds": {"type": "integer", "minimum": 0, "maximum": 600, "description": "Optional wall-clock cap in seconds for the whole dispatch. 0 or omitted uses the role's configured timeout; values above 600 are clamped."},
			"max_turns": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Optional soft cap on tool-using turns. Prefer omitting — the role knows its own budget, and the value cannot go below the role's min_turns."},
			"json_mode": {"type": "boolean", "description": "Request structured JSON output from the sub-agent."},
			"output_limit": {"type": "integer", "minimum": 1024, "description": "Optional byte cap on the sub-agent's final report."},
			"background": {"type": "boolean", "description": "When true, dispatch the sub-agent asynchronously and return immediately. Results arrive in a follow-up message. Returns a job_id you can use with subagent_jobs to check status, cancel, or wait."},
			"seed_from": {"type": "string", "description": "Optional retained proposal id whose sealed workspace seeds this dispatch (retained-result threading); the source proposal stays open. Proposal-lane only."},
			"seed_mode": {"type": "string", "enum": ["workspace", "conversation"], "description": "seed_from only: workspace (default) seeds the sealed worktree state; conversation also seeds the captured messages and is same-role only."},
			"provider": {"type": "string", "description": "Optional provider override for this dispatch (must be a configured provider name). Prefer omitting — the role file or config decides. Omitted falls back to the role's resolution chain."},
			"model": {"type": "string", "description": "Optional model override for this dispatch. Omitted falls back to the role's resolution chain (call > config role entry > role file > parent session model > config default)."}
		},
		"required": ["description", "prompt", "role"]
	}`)
}

// roleNames returns the known role names for validation and schema
// generation: the cached startup snapshot layered with the live resolver
// (if any) so role create/delete via the role tool takes immediate effect.
// Primary-only roles are filtered out (yaah-21uk.4): the result feeds
// both the schema enum and execution-time role validation, and the
// orchestrator can dispatch neither itself nor another primary persona.
func (t *DelegateTool) roleNames() []string {
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
	return subagent.DelegatableRoles(names)
}

// BuildDelegateSchema returns a JSON Schema for the delegate tool whose role
// enum is populated from the given list of role names, so user-defined
// roles discovered at runtime are visible to the model. Primary-only
// roles are excluded from the enum and from the rendered description
// list (yaah-21uk.4) — the orchestrator cannot dispatch them.
func BuildDelegateSchema(roleNames []string, roleDescriptions map[string]string) json.RawMessage {
	// Filter here too, not just roleNames: BuildDelegateSchema is
	// exported, so a primary-only name passed directly must not leak
	// into the enum or the rendered description list.
	roles := subagent.DelegatableRoles(roleNames)

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
				"maximum":     100,
				"description": "Optional iteration cap. Prefer omitting — roles carry tuned budgets, and the value cannot go below the role's min_iterations.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"maximum":     600,
				"description": "Optional wall-clock cap in seconds for the whole dispatch. 0 or omitted uses the role's configured timeout; values above 600 are clamped.",
			},
			"max_turns": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     50,
				"description": "Optional soft cap on tool-using turns. Prefer omitting — the role knows its own budget, and the value cannot go below the role's min_turns.",
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
			"seed_from": map[string]any{
				"type":        "string",
				"description": "Optional retained proposal id whose sealed workspace seeds this dispatch (retained-result threading); the source proposal stays open. Proposal-lane only.",
			},
			"seed_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"workspace", "conversation"},
				"description": "seed_from only: workspace (default) seeds the sealed worktree state; conversation also seeds the captured messages and is same-role only.",
			},
			"provider": map[string]any{
				"type":        "string",
				"description": "Optional provider override for this dispatch (must be a configured provider name). Prefer omitting — the role file or config decides; use only when this dispatch must run elsewhere. Omitted falls back to the role's resolution chain.",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Optional model override for this dispatch. Omitted falls back to the role's resolution chain (call > config role entry > role file > parent session model > config default).",
			},
		},
		"required": []string{"description", "prompt", "role"},
	}
	data, _ := json.Marshal(schema)
	return json.RawMessage(data)
}
