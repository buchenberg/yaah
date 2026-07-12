package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/skills"
)

// SkillTool loads a skill by name and returns its formatted content
// for injection into the agent's conversation.
type SkillTool struct {
	Dirs []string
}

func (t *SkillTool) Name() string { return "skill" }

func (t *SkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "The name of the skill to load"}
		},
		"required": ["name"]
	}`)
}

func (t *SkillTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("skill: invalid arguments: %w", err)
	}
	if params.Name == "" {
		return "", fmt.Errorf("skill: name is required")
	}

	s := skills.FindSkill(t.Dirs, params.Name)
	if s == nil {
		return "", fmt.Errorf("skill %q not found", params.Name)
	}

	return skills.FormatSkillForAgent(s), nil
}
