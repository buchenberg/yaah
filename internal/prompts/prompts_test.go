package prompts

import (
	"runtime"
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

func TestFormatDirectives(t *testing.T) {
	out := FormatDirectives([]string{"run tests", "say jimminy"})
	if !strings.HasPrefix(out, "## Session directives\n\n") {
		t.Errorf("missing section header, got %q", out)
	}
	if !strings.Contains(out, "- run tests\n") || !strings.Contains(out, "- say jimminy\n") {
		t.Errorf("missing bullet entries, got %q", out)
	}
	if !strings.Contains(out, "MANDATORY") || !strings.Contains(out, "supersede any conflicting instructions") {
		t.Errorf("missing precedence framing before the list, got %q", out)
	}
	if !strings.HasSuffix(out, "A response that ignores these directives is a failure, regardless of its other quality.") {
		t.Errorf("missing consequence closing line, got %q", out)
	}
	// The precedence framing must prime the model before the list, not after.
	if idx := strings.Index(out, "supersede"); idx > strings.Index(out, "- run tests") {
		t.Errorf("precedence framing must appear before the directive list, got %q", out)
	}
}

func TestInjectAfterIdentity_PlacedAfterIdentityBlock(t *testing.T) {
	identity := strings.TrimSpace(IdentityPrompt)
	prompt := identity + "\n\n## Environment\nOS: linux"
	got := InjectAfterIdentity(prompt, []string{"always run tests"})

	idxIdentity := strings.Index(got, identity)
	idxDirectives := strings.Index(got, "## Session directives")
	idxEnv := strings.Index(got, "## Environment")
	if idxIdentity < 0 || idxDirectives < 0 || idxEnv < 0 {
		t.Fatalf("missing expected sections in %q", got)
	}
	if !(idxIdentity < idxDirectives && idxDirectives < idxEnv) {
		t.Errorf("directives must sit between identity and environment; order identity=%d directives=%d env=%d", idxIdentity, idxDirectives, idxEnv)
	}
}

func TestInjectAfterIdentity_FallbackAppends(t *testing.T) {
	got := InjectAfterIdentity("custom prompt without identity", []string{"d1"})
	if !strings.HasSuffix(got, FormatDirectives([]string{"d1"})) {
		t.Errorf("directives should be appended for non-identity prompts, got %q", got)
	}
	if !strings.HasPrefix(got, "custom prompt without identity\n\n") {
		t.Errorf("original prompt must be preserved verbatim, got %q", got)
	}
}

func TestInjectAfterIdentity_EmptyDirectivesNoop(t *testing.T) {
	if got := InjectAfterIdentity("prompt", nil); got != "prompt" {
		t.Errorf("nil directives should return prompt unchanged, got %q", got)
	}
	if got := InjectAfterIdentity("prompt", []string{}); got != "prompt" {
		t.Errorf("empty directives should return prompt unchanged, got %q", got)
	}
}

func TestInjectAfterIdentity_EmptyPrompt(t *testing.T) {
	got := InjectAfterIdentity("", []string{"d1"})
	if !strings.HasPrefix(got, "## Session directives") {
		t.Errorf("empty prompt should yield just the directives block, got %q", got)
	}
}

func TestDetectEnvironment_RendersSharedTemplate(t *testing.T) {
	got := DetectEnvironment("/work/dir")
	if !strings.HasPrefix(got, "## Environment\n") {
		t.Errorf("environment block must carry the template heading, got %q", got)
	}
	for _, want := range []string{runtime.GOOS, runtime.GOARCH, "/work/dir", "use it for all shell commands"} {
		if !strings.Contains(got, want) {
			t.Errorf("environment block missing %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Errorf("unreplaced template placeholders remain, got %q", got)
	}
}

func TestBuild_EnvironmentNotDoubleWrapped(t *testing.T) {
	out := Build(Layers{Identity: "I am yaah.", Environment: DetectEnvironment("/cwd")})
	if strings.Contains(out, "## Runtime Environment") {
		t.Errorf("Build must not add a second heading around the environment block, got %q", out)
	}
	if n := strings.Count(out, "## Environment"); n != 1 {
		t.Errorf("expected exactly one environment heading, got %d in %q", n, out)
	}
}

func TestSubAgentIdentityMirrorsCanonicalHabitSections(t *testing.T) {
	// The batch-rule and reason-before-reading sections are duplicated from
	// identity.md on purpose: sub-agents need the habits but not the rest of
	// the orchestrator identity. This test turns silent drift into a failure —
	// when identity.md wording changes, update subagent_identity.md to match.
	identity := strings.TrimSpace(IdentityPrompt)
	sub := strings.TrimSpace(SubAgentIdentityPrompt)

	extract := func(s, start string) string {
		i := strings.Index(s, start)
		if i < 0 {
			t.Fatalf("section %q not found", start)
		}
		rest := s[i:]
		if j := strings.Index(rest, "\n## "); j >= 0 {
			rest = rest[:j]
		}
		return strings.TrimSpace(rest)
	}

	for _, section := range []string{
		"## Cardinal rule: batch tool calls",
		"### Reason before reading",
	} {
		canonical := extract(identity, section)
		if !strings.Contains(sub, canonical) {
			t.Errorf("sub-agent identity must carry the canonical %q section verbatim from identity.md.\nidentity.md has:\n%s\n\nsubagent_identity.md diverged:\n%s", section, canonical, sub)
		}
	}
}

func TestIsolatedWorkspaceGuidance(t *testing.T) {
	got := IsolatedWorkspaceGuidance(
		[]string{"background_process", "go_refactor", "role", "supervised_task"},
		[]string{"mcp_a", "mcp_b"})

	for _, name := range []string{"background_process", "go_refactor", "role", "supervised_task"} {
		if !strings.Contains(got, "`"+name+"`") {
			t.Errorf("guidance must name the omitted tool %s:\n%s", name, got)
		}
	}
	if !strings.Contains(got, "apply_out") {
		t.Error("guidance must explain the apply_out workflow")
	}
	if !strings.Contains(got, "dry_run") {
		t.Error("guidance must point at the dry-run preview")
	}
	if !strings.Contains(got, "spawn_subagent") {
		t.Error("guidance must name the sub-agent alternative for supervised_task")
	}
	if !strings.Contains(got, "`mcp_a`") || !strings.Contains(got, "`mcp_b`") {
		t.Error("guidance must name the omitted MCP tools")
	}
	if !strings.Contains(got, "read-only") || !strings.Contains(got, "`skill`") || !strings.Contains(got, "`plan`") {
		t.Error("guidance must state the skill/plan read-only constraint")
	}

	// An unknown name (a future host-only tool) must still be listed.
	if got := IsolatedWorkspaceGuidance([]string{"some_future_tool"}, nil); !strings.Contains(got, "`some_future_tool`") {
		t.Errorf("unknown omitted tool must be listed, got:\n%s", got)
	}
	// Empty omission lists are valid: the mode explanation still applies
	// (apply_out is always registered in isolated mode).
	if got := IsolatedWorkspaceGuidance(nil, nil); strings.Contains(got, "NOT available here") {
		t.Errorf("no omitted tools must not print an empty unavailable list, got:\n%s", got)
	}
}

func TestIsolatedSubAgentGuidance(t *testing.T) {
	got := IsolatedSubAgentGuidance([]string{"go_refactor"})

	if !strings.Contains(got, "isolated container") {
		t.Errorf("guidance must say the sub-agent runs in the container:\n%s", got)
	}
	if !strings.Contains(got, "never claim they are saved") {
		t.Errorf("guidance must warn against claiming changes are saved:\n%s", got)
	}
	if !strings.Contains(got, "`go_refactor`") {
		t.Errorf("guidance must name the dropped host-only tool:\n%s", got)
	}
	// The sub-agent cannot call apply_out (it is not a leaf tool and role
	// profiles cannot name it); the guidance must not instruct it to.
	if strings.Contains(got, "call `apply_out`") {
		t.Errorf("guidance must leave the apply_out workflow to the parent session:\n%s", got)
	}

	// A role with no dropped tools gets the mode explanation without an
	// unavailable list.
	if bare := IsolatedSubAgentGuidance(nil); strings.Contains(bare, "NOT available here") {
		t.Errorf("no dropped tools must not print an unavailable list:\n%s", bare)
	}
}
