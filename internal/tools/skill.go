package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/skills"
)

// SkillTool loads, creates, edits, and searches skills.
type SkillTool struct {
	Dirs     []string
	Embedder memory.Embedder // optional; enables semantic skill search
}

func (t *SkillTool) Name() string { return "skill" }
func (t *SkillTool) Description() string {
	return prompts.ToolDescription("skill")
}

func (t *SkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["load", "list", "create", "edit", "search"], "description": "Action to perform (default: load)"},
			"name": {"type": "string", "description": "Skill name (required for load, create, edit)"},
			"description": {"type": "string", "description": "Skill description (required for create, optional for edit)"},
			"body": {"type": "string", "description": "Skill body in markdown (required for create, optional for edit)"},
			"query": {"type": "string", "description": "Natural-language search query for semantic skill discovery (required for search)"}
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
		Query       string `json:"query"`
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

	case "search":
		if params.Query == "" {
			return "", fmt.Errorf("skill: query is required for search")
		}
		found := skills.Discover(t.Dirs)
		if len(found) == 0 {
			return "No skills found.", nil
		}
		return skillSearch(ctx, t.Embedder, params.Query, found), nil

	default:
		return "", fmt.Errorf("skill: unknown action %q", params.Action)
	}
}

type skillMatch struct {
	skill *skills.Skill
	score float64
}

func skillSearch(ctx context.Context, emb memory.Embedder, query string, found []skills.Skill) string {
	searchText := func(s skills.Skill) string { return s.Name + ": " + s.Description }

	if emb != nil {
		qEmb, err := emb.Embed(ctx, query)
		if err != nil {
			return keywordSearch(query, found, searchText)
		}

		embeddings := make([]memory.Embedding, len(found))
		for i, s := range found {
			embeddings[i], _ = emb.Embed(ctx, searchText(s))
		}

		matches := make([]skillMatch, len(found))
		for i := range found {
			score := float64(0)
			if embeddings[i] != nil {
				score = float64(memory.CosineSimilarity(qEmb, embeddings[i]))
			}
			matches[i] = skillMatch{skill: &found[i], score: score}
		}

		sort.Slice(matches, func(i, j int) bool { return matches[i].score > matches[j].score })

		return formatSearchResults(matches, true)
	}

	return keywordSearch(query, found, searchText)
}

func keywordSearch(query string, found []skills.Skill, searchText func(skills.Skill) string) string {
	q := strings.ToLower(query)
	matches := make([]skillMatch, 0, len(found))
	for i := range found {
		s := found[i]
		text := strings.ToLower(searchText(s))
		body := strings.ToLower(s.Body)
		score := 0.0
		if strings.Contains(text, q) {
			score = 0.8
		}
		if strings.Contains(body, q) {
			score = max(score, 0.5)
		}
		if score > 0 {
			matches = append(matches, skillMatch{skill: &found[i], score: score})
		}
	}
	if len(matches) == 0 {
		return fmt.Sprintf("No skills matched query %q.", query)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	return formatSearchResults(matches, false)
}

func formatSearchResults(matches []skillMatch, showScore bool) string {
	topK := len(matches)
	if topK > 10 {
		topK = 10
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d matching skill(s):\n", len(matches)))
	for i := 0; i < topK; i++ {
		m := matches[i]
		if showScore {
			sb.WriteString(fmt.Sprintf("  - %s (%.2f): %s\n", m.skill.Name, m.score, m.skill.Description))
		} else {
			sb.WriteString(fmt.Sprintf("  - %s: %s\n", m.skill.Name, m.skill.Description))
		}
	}
	return sb.String()
}
