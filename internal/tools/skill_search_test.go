package tools

// skill_search_test.go pins the semantic skill-search contract (yaah-92p): a
// relevance floor so a non-match is reportable, per-skill embedding errors
// surfaced instead of scored as zero, and the keyword fallback labelled when
// the query itself cannot be embedded.

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/skills"
)

// vectorEmbedder returns a mapped vector or error for exact input texts.
// Anything unmapped gets fallback (a zero vector scores 0).
type vectorEmbedder struct {
	vectors  map[string]memory.Embedding
	errs     map[string]error
	fallback memory.Embedding
}

func (v vectorEmbedder) Embed(_ context.Context, text string) (memory.Embedding, error) {
	if err, ok := v.errs[text]; ok {
		return nil, err
	}
	if vec, ok := v.vectors[text]; ok {
		return vec, nil
	}
	return v.fallback, nil
}

func skillSearchFixture() []skills.Skill {
	return []skills.Skill{
		{Name: "postgres", Description: "database queries"},
		{Name: "css", Description: "layout and styling"},
	}
}

func TestSkillSearch_AllBelowFloorIsAMiss(t *testing.T) {
	// The query must not appear literally in any skill: a literal match is a
	// real hit via the keyword backstop, even below the semantic floor.
	emb := vectorEmbedder{vectors: map[string]memory.Embedding{
		"elephants":                  {1, 0},
		"postgres: database queries": {0.3, 1}, // cosine ≈ 0.29, below the 0.35 floor
		"css: layout and styling":    {0, 1},
	}}

	out := skillSearch(context.Background(), emb, "elephants", skillSearchFixture())
	if !strings.Contains(out, `No skills matched query "elephants"`) {
		t.Fatalf("an all-below-floor search must report a miss: %q", out)
	}
	if !strings.Contains(out, "best score 0.29") || !strings.Contains(out, "floor 0.35") {
		t.Fatalf("miss must report the best score and the floor: %q", out)
	}
	if strings.Contains(out, "Found") {
		t.Fatalf("below-floor skills reported as matches: %q", out)
	}
}

func TestSkillSearch_MixAboveAndBelowFloor(t *testing.T) {
	emb := vectorEmbedder{vectors: map[string]memory.Embedding{
		"database":                   {1, 0},
		"postgres: database queries": {1, 0},
		"css: layout and styling":    {0, 1},
	}}

	out := skillSearch(context.Background(), emb, "database", skillSearchFixture())
	if !strings.Contains(out, "Found 1 matching skill(s):") {
		t.Fatalf("only the above-floor skill is a match: %q", out)
	}
	if !strings.Contains(out, "postgres") {
		t.Fatalf("above-floor skill missing: %q", out)
	}
	if strings.Contains(out, "  - css") {
		t.Fatalf("below-floor skill reported as a match: %q", out)
	}
}

func TestSkillSearch_PerSkillEmbedErrorSurfaced(t *testing.T) {
	emb := vectorEmbedder{
		vectors: map[string]memory.Embedding{
			"database":                   {1, 0},
			"postgres: database queries": {1, 0},
		},
		errs: map[string]error{"css: layout and styling": errors.New("embed timeout")},
	}

	out := skillSearch(context.Background(), emb, "database", skillSearchFixture())
	if !strings.Contains(out, "1 skill(s) could not be embedded: css: embed timeout") {
		t.Fatalf("per-skill embed error not surfaced: %q", out)
	}
	if !strings.Contains(out, "Found 1 matching skill(s):") {
		t.Fatalf("the embeddable skill should still be reported: %q", out)
	}
	if strings.Contains(out, "  - css") {
		t.Fatalf("a failed embedding was scored as a match: %q", out)
	}
}

func TestSkillSearch_QueryEmbedFailureLabelledFallback(t *testing.T) {
	emb := vectorEmbedder{errs: map[string]error{"database": errors.New("connection refused")}}

	out := skillSearch(context.Background(), emb, "database", skillSearchFixture())
	if !strings.HasPrefix(out, "semantic search unavailable: connection refused\n") {
		t.Fatalf("keyword fallback not labelled with the embed error: %q", out)
	}
	if !strings.Contains(out, "Found 1 matching skill(s):") || !strings.Contains(out, "postgres") {
		t.Fatalf("keyword fallback lost its match: %q", out)
	}
}

func TestSkillSearch_QueryEmbedFailureAndNoKeywordMatch(t *testing.T) {
	emb := vectorEmbedder{errs: map[string]error{"quantum": errors.New("no route to host")}}

	out := skillSearch(context.Background(), emb, "quantum", skillSearchFixture())
	if !strings.Contains(out, "semantic search unavailable: no route to host") {
		t.Fatalf("fallback not labelled: %q", out)
	}
	if !strings.Contains(out, `No skills matched query "quantum".`) {
		t.Fatalf("keyword miss not reported: %q", out)
	}
}

func TestSkillExecute_NoSkillsDiscovered(t *testing.T) {
	// Discovery short-circuit: no skill is found, so Execute answers before
	// skillSearch runs. The semantic-path zero-candidate case is covered by
	// TestSkillSearch_NoSkills below.
	tool := &SkillTool{Dirs: []string{t.TempDir()}}
	out, err := tool.Execute(context.Background(), `{"action":"search","query":"database"}`)
	if err != nil {
		t.Fatalf("skill search: %v", err)
	}
	if out != "No skills found." {
		t.Fatalf("empty discovery = %q, want the explicit no-skills message", out)
	}
}

func TestSkillSearch_NoSkills(t *testing.T) {
	out := skillSearch(context.Background(), vectorEmbedder{}, "database", nil)
	if out != "No skills found." {
		t.Fatalf("zero candidates = %q, want the explicit no-skills message", out)
	}
}

// TestSkillSearch_KeywordMatchSurvivesBelowFloor pins the literal-match union:
// a skill whose description contains the query is a real hit even when the
// embedding model scores it below the floor, so the floor cannot regress the
// keyword matcher.
func TestSkillSearch_KeywordMatchSurvivesBelowFloor(t *testing.T) {
	emb := vectorEmbedder{vectors: map[string]memory.Embedding{
		"database":                   {1, 0},
		"postgres: database queries": {0, 1}, // cosine 0, below the floor
		"css: layout and styling":    {0, 1},
	}}

	out := skillSearch(context.Background(), emb, "database", skillSearchFixture())
	if !strings.Contains(out, "Found 1 matching skill(s):") || !strings.Contains(out, "postgres") {
		t.Fatalf("literal keyword match must survive a below-floor semantic score: %q", out)
	}
	if strings.Contains(out, "No skills matched") {
		t.Fatalf("literal keyword match reported as a miss: %q", out)
	}
}

// TestSkillSearch_KeywordMatchIsUnionedWithSemanticMatches: the literal match
// must survive even when a *different* skill clears the floor, not only when
// nothing does.
func TestSkillSearch_KeywordMatchIsUnionedWithSemanticMatches(t *testing.T) {
	found := []skills.Skill{
		{Name: "postgres", Description: "database queries"},
		{Name: "database-backup", Description: "database restore runbook"},
	}
	emb := vectorEmbedder{vectors: map[string]memory.Embedding{
		"database":                   {1, 0},
		"postgres: database queries": {1, 0}, // clears the floor
		"database-backup: database restore runbook": {0, 1}, // literal but below floor
	}}

	out := skillSearch(context.Background(), emb, "database", found)
	if !strings.Contains(out, "Found 2 matching skill(s):") {
		t.Fatalf("literal match must be unioned with the semantic hit: %q", out)
	}
	for _, name := range []string{"postgres", "database-backup"} {
		if !strings.Contains(out, name) {
			t.Errorf("unioned result lost %q: %q", name, out)
		}
	}
	if !strings.Contains(out, "database-backup [keyword]") {
		t.Errorf("literal hit must be marked as keyword, not printed on the cosine scale: %q", out)
	}
	if strings.Contains(out, "database-backup (") {
		t.Errorf("literal hit rendered with a synthetic numeric score: %q", out)
	}
}

// TestSkillSearch_AllEmbeddingsFailFallsBackToKeyword: when every embedding
// fails there is no honest "best score"; the literal keyword matches still
// surface and every failure is named.
func TestSkillSearch_AllEmbeddingsFailFallsBackToKeyword(t *testing.T) {
	emb := vectorEmbedder{
		vectors: map[string]memory.Embedding{"database": {1, 0}},
		errs: map[string]error{
			"postgres: database queries": errors.New("embed timeout"),
			"css: layout and styling":    errors.New("embed timeout"),
		},
	}

	out := skillSearch(context.Background(), emb, "database", skillSearchFixture())
	if !strings.Contains(out, "postgres") || !strings.Contains(out, "database queries") {
		t.Fatalf("literal keyword match lost when all embeddings failed: %q", out)
	}
	if !strings.Contains(out, "2 skill(s) could not be embedded:") {
		t.Fatalf("embed failures not counted: %q", out)
	}
	if strings.Contains(out, "best score") {
		t.Fatalf("an unscored search must not fabricate a best score: %q", out)
	}
}

func TestSkillSearch_AllEmbeddingsFailWithoutKeywordMatch(t *testing.T) {
	emb := vectorEmbedder{
		vectors: map[string]memory.Embedding{"quantum": {1, 0}},
		errs: map[string]error{
			"postgres: database queries": errors.New("embed timeout"),
			"css: layout and styling":    errors.New("embed timeout"),
		},
	}

	out := skillSearch(context.Background(), emb, "quantum", skillSearchFixture())
	if !strings.Contains(out, "no skill could be scored") {
		t.Fatalf("all-failed embeddings must be reported as unscored: %q", out)
	}
	if strings.Contains(out, "best score 0.00") {
		t.Fatalf("fabricated best score on an unscored search: %q", out)
	}
	if !strings.Contains(out, "2 skill(s) could not be embedded:") {
		t.Fatalf("embed failures not counted: %q", out)
	}
}

// TestSkillSearch_NegativeBestScore: cosine similarity is negative for
// opposite vectors, so the miss message must report the real best, not 0.00.
func TestSkillSearch_NegativeBestScore(t *testing.T) {
	emb := vectorEmbedder{vectors: map[string]memory.Embedding{
		"quantum":                    {1, 0},
		"postgres: database queries": {-1, 0},
		"css: layout and styling":    {-1, 0},
	}}

	out := skillSearch(context.Background(), emb, "quantum", skillSearchFixture())
	if !strings.Contains(out, "best score -1.00") {
		t.Fatalf("negative best score understated: %q", out)
	}
}

// TestSkillSearch_NonFiniteScoreIsNotAMatch: an overflowing cosine produces
// NaN/Inf, which `score < floor` would let through. It must be treated as
// unscored, never ranked as a match, and must not be described as an embed
// failure.
func TestSkillSearch_NonFiniteScoreIsNotAMatch(t *testing.T) {
	inf := float32(math.Inf(1))
	emb := vectorEmbedder{vectors: map[string]memory.Embedding{
		"quantum":                    {inf, 0},
		"postgres: database queries": {inf, 0},
		"css: layout and styling":    {inf, 0},
	}}

	out := skillSearch(context.Background(), emb, "quantum", skillSearchFixture())
	if !strings.Contains(out, `No skills matched query "quantum": no skill could be scored.`) {
		t.Fatalf("non-finite scores must produce an explicit unscored miss: %q", out)
	}
	if strings.Contains(out, "Found ") {
		t.Fatalf("a non-finite similarity passed the floor as a match: %q", out)
	}
	if !strings.Contains(out, "2 skill(s) had a non-finite similarity score") {
		t.Fatalf("non-finite scores must be surfaced separately: %q", out)
	}
	if strings.Contains(out, "could not be embedded") {
		t.Fatalf("a computed non-finite score is not an embed failure: %q", out)
	}
}
