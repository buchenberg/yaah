package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/prompts"
)

// SubAgentInfo describes a single sub-agent role for the list_subagents tool.
type SubAgentInfo struct {
	Role        string           `json:"role"`
	DisplayName string           `json:"display_name"`
	Specialty   string           `json:"specialty"`
	Contract    SubAgentContract `json:"contract"`
	Description string           `json:"description"`
	Tools       []string         `json:"tools"`
}

// SubAgentContract mirrors the YAML contract definition from role files
// but uses simplified types for JSON serialization.
type SubAgentContract struct {
	Heading string                   `json:"heading"`
	Fields  []subagent.ContractField `json:"fields"`
}

// SubAgentLister returns role metadata so the agent can see available
// sub-agent roles and their tool sets. Called by ListSubAgentsTool.
type SubAgentLister func() []SubAgentInfo

// ListSubAgentsTool returns the available sub-agent roles and their
// capabilities so the orchestrating agent can make informed dispatch
// decisions.
type ListSubAgentsTool struct {
	Lister SubAgentLister
}

func (t *ListSubAgentsTool) Name() string { return "list_subagents" }
func (t *ListSubAgentsTool) Description() string {
	return prompts.ToolDescription("list_subagents")
}
func (t *ListSubAgentsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
}

func (t *ListSubAgentsTool) Execute(ctx context.Context, args string) (string, error) {
	if t.Lister == nil {
		return "", fmt.Errorf("list_subagents: lister not configured")
	}
	roles := t.Lister()
	if len(roles) == 0 {
		return "No sub-agent roles registered. Use the default role for full tool access.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d sub-agent role(s) available:\n\n", len(roles))
	for _, r := range roles {
		header := r.DisplayName
		if header == "" {
			header = r.Role
		}
		if r.Specialty != "" {
			header += " — " + r.Specialty
		}
		fmt.Fprintf(&b, "## %s\n", header)
		fmt.Fprintf(&b, "Role ID: %s\n", r.Role)
		if r.Contract.Heading != "" {
			var fieldStrs []string
			for _, f := range r.Contract.Fields {
				if f.Kind != "" {
					fieldStrs = append(fieldStrs, fmt.Sprintf("%s (%s)", f.Name, f.Kind))
				} else {
					fieldStrs = append(fieldStrs, f.Name)
				}
			}
			fmt.Fprintf(&b, "Contract: %s → %s\n", r.Contract.Heading, strings.Join(fieldStrs, ", "))
		}
		if r.Description != "" {
			fmt.Fprintf(&b, "%s\n\n", r.Description)
		}
		fmt.Fprintf(&b, "Tools: %s\n\n", strings.Join(r.Tools, ", "))
	}
	return strings.TrimSpace(b.String()), nil
}
