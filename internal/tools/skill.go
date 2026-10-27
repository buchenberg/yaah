package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
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
	// Strict enables the autonomous-write guardrail: before create/edit
	// writes, the skill body is run through skills.ScanSecurity, which
	// rejects fetch-and-execute, destructive-delete, fork-bomb, and
	// disk-destroyer constructs. Agent-session wiring sets it so that
	// unattended consolidation cannot smuggle an attack chain into a skill
	// a later run will load and follow (docs/compaction-as-sleep.md §8 P2).
	Strict bool
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
		if t.Strict {
			// Scan the whole serialized skill, not just the body: name and
			// description are interpolated into the written SKILL.md, so a
			// dangerous pattern there lands in the file too.
			if err := skills.ScanSecurity(params.Name + "\n" + params.Description + "\n" + params.Body); err != nil {
				return "", fmt.Errorf("skill create blocked: %w", err)
			}
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
		if t.Strict {
			// Scan the whole serialized skill, falling back to the preserved
			// description/body for empty fields (that is what gets written).
			desc := params.Description
			if desc == "" {
				desc = s.Description
			}
			body := params.Body
			if body == "" {
				body = s.Body
			}
			if err := skills.ScanSecurity(s.Name + "\n" + desc + "\n" + body); err != nil {
				return "", fmt.Errorf("skill edit blocked: %w", err)
			}
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

// skillRelevanceFloor is the minimum cosine similarity for a semantic skill
// hit. Embedding models place loosely related text around 0.2–0.4, so without
// a floor every discovered skill is reported as a match and "Found N matching
// skill(s)" is just the skill count (yaah-92p).
//
// The value is calibrated for the cosine scale shared by common embedding
// servers (nomic/OpenAI-style models score related text around 0.5+ and
// unrelated text around 0.2–0.4). It is a floor, not a match oracle: a server
// whose scale diverges is covered by the keyword backstop below, which keeps
// a literal name/description match reportable even when the model scores it
// low.
const skillRelevanceFloor = 0.35

func skillSearch(ctx context.Context, emb memory.Embedder, query string, found []skills.Skill) string {
	searchText := func(s skills.Skill) string { return s.Name + ": " + s.Description }

	if len(found) == 0 {
		return "No skills found."
	}
	if emb == nil {
		return keywordSearch(query, found, searchText)
	}

	qEmb, err := emb.Embed(ctx, query)
	if err != nil {
		// Keyword recall still works; label the path so a miss is not read as
		// "no such skill" when the semantic path was simply down.
		return fmt.Sprintf("semantic search unavailable: %v\n%s", err, keywordSearch(query, found, searchText))
	}

	matches := make([]skillMatch, 0, len(found))
	var embedErrs []string
	best := math.Inf(-1)
	scored := 0
	for i := range found {
		e, err := emb.Embed(ctx, searchText(found[i]))
		if err != nil {
			// A failed embedding is not a zero score: surfacing it keeps a
			// skill out of the report honestly instead of scoring it as a miss.
			embedErrs = append(embedErrs, fmt.Sprintf("%s: %v", found[i].Name, err))
			continue
		}
		score := float64(memory.CosineSimilarity(qEmb, e))
		if math.IsNaN(score) {
			// A non-finite score cannot be ranked or thresholded; treat it as
			// unscored rather than letting NaN slip past the floor.
			embedErrs = append(embedErrs, found[i].Name+": non-finite similarity")
			continue
		}
		scored++
		if score > best {
			best = score
		}
		if !(score >= skillRelevanceFloor) {
			continue
		}
		matches = append(matches, skillMatch{skill: &found[i], score: score})
	}

	var out string
	switch {
	case len(matches) > 0:
		sort.Slice(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
		out = formatSearchResults(matches, true)
	default:
		if kw := keywordMatches(query, found, searchText); len(kw) > 0 {
			// The floor must not hide a literal match: a skill whose name or
			// description contains the query is a real hit even when the model
			// scores it below the semantic floor.
			sort.Slice(kw, func(i, j int) bool { return kw[i].score > kw[j].score })
			out = formatSearchResults(kw, false)
		} else if scored == 0 {
			// Nothing was actually scored (every embedding failed), so there is
			// no honest "best score" to report.
			out = fmt.Sprintf("No skills matched query %q: no skill could be scored.\n", query)
		} else {
			out = fmt.Sprintf("No skills matched query %q (best score %.2f, floor %.2f).\n", query, best, skillRelevanceFloor)
		}
	}

	if len(embedErrs) > 0 {
		out += fmt.Sprintf("%d skill(s) could not be embedded: %s\n", len(embedErrs), strings.Join(embedErrs, "; "))
	}
	return out
}

// keywordMatches returns the literal name/description/body hits. It is shared
// by keywordSearch and the semantic path's backstop so a below-floor semantic
// miss can still surface a real keyword match.
func keywordMatches(query string, found []skills.Skill, searchText func(skills.Skill) string) []skillMatch {
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
	return matches
}

func keywordSearch(query string, found []skills.Skill, searchText func(skills.Skill) string) string {
	matches := keywordMatches(query, found, searchText)
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
