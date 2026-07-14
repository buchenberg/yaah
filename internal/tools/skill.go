package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/skills"
)

// SkillTool loads, creates, and edits skills.
type SkillTool struct {
	Dirs []string
}

func (t *SkillTool) Name() string { return "skill" }
func (t *SkillTool) Description() string {
	return "Load, list, create, or edit skills. Skills are SKILL.md files with YAML frontmatter and markdown instructions."
}

func (t *SkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["load", "list", "create", "edit"], "description": "Action to perform (default: load)"},
			"name": {"type": "string", "description": "Skill name (required for load, create, edit)"},
			"description": {"type": "string", "description": "Skill description (required for create, optional for edit)"},
			"body": {"type": "string", "description": "Skill body in markdown (required for create, optional for edit)"}
		},
		"required": ["action"]
	}`)
}

func (t *SkillTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Action      string `json:"action"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("skill: invalid arguments: %w", err)
	}
	if params.Action == "" {
		params.Action = "load"
	}

	switch params.Action {
	case "load":
		if params.Name == "" {
			return "", fmt.Errorf("skill: name is required for load")
		}
		s := skills.FindSkill(t.Dirs, params.Name)
		if s == nil {
			return "", fmt.Errorf("skill %q not found", params.Name)
		}
		return skills.FormatSkillForAgent(s), nil

	case "list":
		found := skills.Discover(t.Dirs)
		if len(found) == 0 {
			return "No skills found.", nil
		}
		out := fmt.Sprintf("Found %d skill(s):\n", len(found))
		for _, s := range found {
			out += fmt.Sprintf("  - %s: %s\n", s.Name, s.Description)
		}
		return out, nil

	case "create":
		if params.Name == "" {
			return "", fmt.Errorf("skill: name is required for create")
		}
		if params.Description == "" {
			return "", fmt.Errorf("skill: description is required for create")
		}
		if params.Body == "" {
			return "", fmt.Errorf("skill: body is required for create")
		}
		// Create in the first search path (project-level .agents/skills/)
		if len(t.Dirs) == 0 {
			return "", fmt.Errorf("skill: no search paths configured")
		}
		path, err := skills.Create(t.Dirs[0], params.Name, params.Description, params.Body)
		if err != nil {
			return "", fmt.Errorf("skill create: %w", err)
		}
		return fmt.Sprintf("Skill %q created at %s", params.Name, path), nil

	case "edit":
		if params.Name == "" {
			return "", fmt.Errorf("skill: name is required for edit")
		}
		s := skills.FindSkill(t.Dirs, params.Name)
		if s == nil {
			return "", fmt.Errorf("skill %q not found", params.Name)
		}
		path, err := skills.Edit(s, params.Description, params.Body)
		if err != nil {
			return "", fmt.Errorf("skill edit: %w", err)
		}
		return fmt.Sprintf("Skill %q updated at %s", params.Name, path), nil

	default:
		return "", fmt.Errorf("skill: unknown action %q", params.Action)
	}
}
