package tools

// skill_search_test.go pins the semantic skill-search contract (yaah-92p): a
// relevance floor so a non-match is reportable, per-skill embedding errors
// surfaced instead of scored as zero, and the keyword fallback labelled when
// the query itself cannot be embedded.

import (
	"context"
	"errors"
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
	emb := vectorEmbedder{vectors: map[string]memory.Embedding{
		"database":                   {1, 0},
		"postgres: database queries": {0.3, 1}, // cosine ≈ 0.29, below the 0.35 floor
		"css: layout and styling":    {0, 1},
	}}

	out := skillSearch(context.Background(), emb, "database", skillSearchFixture())
	if !strings.Contains(out, `No skills matched query "database"`) {
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

func TestSkillSearch_NoSkillsDiscovered(t *testing.T) {
	tool := &SkillTool{Dirs: []string{t.TempDir()}, Embedder: vectorEmbedder{}}
	out, err := tool.Execute(context.Background(), `{"action":"search","query":"database"}`)
	if err != nil {
		t.Fatalf("skill search: %v", err)
	}
	if out != "No skills found." {
		t.Fatalf("empty discovery = %q, want the explicit no-skills message", out)
	}
}
