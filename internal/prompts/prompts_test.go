package prompts

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/skills"
)

func TestTruncateSkillDesc_ShortUnchanged(t *testing.T) {
	got := truncateSkillDesc("Run a standard multi-step benchmark.", "fallback")
	if got != "Run a standard multi-step benchmark." {
		t.Errorf("short description should be unchanged, got %q", got)
	}
}

func TestTruncateSkillDesc_LongCappedWithEllipsis(t *testing.T) {
	long := "Charm Bubbles v2 — TUI primitive components for Bubble Tea v2 in the yaah TUI. Load when adding or modifying any bubbles component in yaah's TUI, or when migrating v1 bubbles code to v2."
	got := truncateSkillDesc(long, "fallback")
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated description should end with ellipsis, got %q", got)
	}
	// body (without the ellipsis) must not exceed the cap in runes
	body := []rune(strings.TrimSuffix(got, "…"))
	if len(body) > skillDescMaxChars {
		t.Errorf("truncated body exceeds cap: %d runes (got %q)", len(body), got)
	}
	if len(body) > 0 && body[len(body)-1] == ' ' {
		t.Errorf("truncated description should not end with a space, got %q", got)
	}
}

func TestTruncateSkillDesc_FirstLineOnly(t *testing.T) {
	multi := "First line is the summary.\nSecond line has more detail that should be dropped entirely."
	got := truncateSkillDesc(multi, "fallback")
	if strings.Contains(got, "Second line") {
		t.Errorf("multi-line description should keep only the first line, got %q", got)
	}
	if !strings.Contains(got, "First line is the summary") {
		t.Errorf("expected the first line preserved, got %q", got)
	}
}

func TestTruncateSkillDesc_EmptyFallsBack(t *testing.T) {
	if got := truncateSkillDesc("", "my-skill"); got != "my-skill" {
		t.Errorf("empty description should fall back to the name, got %q", got)
	}
	if got := truncateSkillDesc("   \n  ", "my-skill"); got != "my-skill" {
		t.Errorf("whitespace-only description should fall back, got %q", got)
	}
}

func TestTruncateSkillDesc_MultibyteNotSplit(t *testing.T) {
	// Leading em-dashes and accented chars must not be split mid-rune.
	long := strings.Repeat("é", skillDescMaxChars+50)
	got := truncateSkillDesc(long, "fallback")
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis on long multibyte input, got %q", got)
	}
}

func TestBuildSkillsIndex_TruncatesEach(t *testing.T) {
	long := strings.Repeat("word ", 60) // well over the cap, no newline
	list := []skills.Skill{
		{Name: "zebra", Description: "short desc"},
		{Name: "alpha", Description: long},
		{Name: "mid", Description: ""}, // empty -> name fallback
	}
	out := BuildSkillsIndex(list)
	if !strings.HasPrefix(out, "## Available Skills\n") {
		t.Errorf("missing header, got %q", out)
	}
	// Sorted alphabetically by name.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")[1:]
	if len(lines) != 3 || !strings.Contains(lines[0], "alpha") {
		t.Errorf("expected sorted entries, got %v", lines)
	}
	// The long entry must be capped; the short one preserved verbatim.
	var alphaLine, zebraLine string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "alpha"):
			alphaLine = l
		case strings.Contains(l, "zebra"):
			zebraLine = l
		}
	}
	if !strings.HasSuffix(alphaLine, "…") {
		t.Errorf("long description should be truncated with ellipsis, got %q", alphaLine)
	}
	if !strings.Contains(zebraLine, "short desc") {
		t.Errorf("short description should be preserved verbatim, got %q", zebraLine)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "- **mid**: mid") {
		t.Errorf("empty description should fall back to name, got %v", lines)
	}
}

func TestBuildSkillsIndex_Empty(t *testing.T) {
	if out := BuildSkillsIndex(nil); out != "" {
		t.Errorf("expected empty string for no skills, got %q", out)
	}
}
