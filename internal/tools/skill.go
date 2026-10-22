package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/skills"
)

// SkillTool loads, creates, edits, archives, and searches skills.
type SkillTool struct {
	Dirs     []string
	Embedder memory.Embedder // optional; enables semantic skill search
	// Store is the optional skill-lifecycle store: usage counters,
	// provenance, and the append-only mutation ledger
	// (docs/compaction-as-sleep.md §8 P1). Nil disables tracking
	// entirely — the tool then behaves exactly as it did before P1.
	Store *memory.DB
	// ReadOnly disables the mutating actions (create, edit). Isolated
	// sessions set it: skills are host harness configuration (.agents/
	// directories the host reads), so writing them from inside the
	// container would mutate tracked host files around the apply-out
	// boundary. Reading — load, list, search — stays available.
	ReadOnly bool
}

// readOnlyActionError is the shared refusal for a mutating action on a
// read-only skill or plan tool.
func readOnlyActionError(tool, action string) error {
	return fmt.Errorf("%s: %s is disabled in this session — skills and plans are host harness configuration, and isolated mode must not write host files outside apply_out", tool, action)
}

func (t *SkillTool) Name() string { return "skill" }
func (t *SkillTool) Description() string {
	return prompts.ToolDescription("skill")
}

func (t *SkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["load", "list", "create", "edit", "delete", "search"], "description": "Action to perform (default: load)"},
			"name": {"type": "string", "description": "Skill name (required for load, create, edit, delete)"},
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
		// A load is a view, not a use: whether the skill was actually
		// applied is only knowable later (P3).
		t.track("skill_usage", params.Name, func(db *memory.DB) error {
			return db.RecordSkillUsage(params.Name, false)
		})
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
		if t.ReadOnly {
			return "", readOnlyActionError("skill", "create")
		}
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
		// Hash the file as written (re-parsed) so the create row's after_hash
		// chains into the next edit row's before_hash, which re-reads the file.
		after := skillContentHash(params.Name, params.Description, params.Body)
		if created := skills.FindSkill(t.Dirs, params.Name); created != nil {
			after = skillContentHash(created.Name, created.Description, created.Body)
		}
		t.track("skill_provenance", params.Name, func(db *memory.DB) error {
			return db.RecordSkillProvenance(params.Name, "agent", "skill:create")
		})
		t.track("skill_ledger", params.Name, func(db *memory.DB) error {
			return db.RecordSkillLedger(params.Name, "create", "", after, "")
		})
		return fmt.Sprintf("Skill %q created at %s", params.Name, path), nil

	case "edit":
		if t.ReadOnly {
			return "", readOnlyActionError("skill", "edit")
		}
		if params.Name == "" {
			return "", fmt.Errorf("skill: name is required for edit")
		}
		s := skills.FindSkill(t.Dirs, params.Name)
		if s == nil {
			return "", fmt.Errorf("skill %q not found", params.Name)
		}
		before := skillContentHash(s.Name, s.Description, s.Body)
		path, err := skills.Edit(s, params.Description, params.Body)
		if err != nil {
			return "", fmt.Errorf("skill edit: %w", err)
		}
		if t.Store != nil {
			// Hash the file as written, not the arguments: Edit falls back
			// to the current frontmatter for empty fields, so the effective
			// content is not always what the caller passed.
			after := before
			if updated := skills.FindSkill(t.Dirs, params.Name); updated != nil {
				after = skillContentHash(updated.Name, updated.Description, updated.Body)
			}
			t.track("skill_ledger", params.Name, func(db *memory.DB) error {
				return db.RecordSkillLedger(params.Name, "edit", before, after, "")
			})
		}
		return fmt.Sprintf("Skill %q updated at %s", params.Name, path), nil

	case "delete":
		if t.ReadOnly {
			return "", readOnlyActionError("skill", "delete")
		}
		if params.Name == "" {
			return "", fmt.Errorf("skill: name is required for delete")
		}
		s := skills.FindSkill(t.Dirs, params.Name)
		if s == nil {
			return "", fmt.Errorf("skill %q not found", params.Name)
		}
		hash := skillContentHash(s.Name, s.Description, s.Body)
		archived, err := skills.Archive(s)
		if err != nil {
			return "", fmt.Errorf("skill archive: %w", err)
		}
		t.track("skill_ledger", params.Name, func(db *memory.DB) error {
			return db.RecordSkillLedger(params.Name, "archive", hash, "", "")
		})
		return fmt.Sprintf("Skill %q archived (not deleted) at %s — restorable by moving the file back to %s",
			params.Name, archived, s.Path), nil

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

// skillContentHash returns the SHA-256 digest of a skill's content. The
// name is part of the digest so a rename reads as a change; the ledger
// stores only hashes, never bodies.
func skillContentHash(name, description, body string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + description + "\x00" + body))
	return hex.EncodeToString(sum[:])
}

// track runs a best-effort lifecycle write against the optional store. The
// skill file has already changed by the time these run, so a bookkeeping
// failure is logged and never fails the tool call: a lost ledger row must
// not make an applied edit look rejected. A nil Store disables tracking.
func (t *SkillTool) track(op, name string, write func(*memory.DB) error) {
	if t.Store == nil {
		return
	}
	if err := write(t.Store); err != nil {
		slog.Warn("skill: lifecycle write failed", "op", op, "name", name, "err", err)
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
