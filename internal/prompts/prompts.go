// Package prompts provides the embedded system prompt identity and
// assembly functions for constructing the full system prompt from
// multiple layers: embedded identity, user context, project context,
// memory, and skills.
package prompts

import (
	"embed"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/skills"
)

// IdentityPrompt is the default system identity shipped with the yaah
// binary. It defines what yaah is, its principles, capabilities, and
// behavioral rules.
//
//go:embed identity.md
var IdentityPrompt string

//go:embed summary_template.md
var summaryTemplate string

//go:embed contract_rules.md
var contractRules string

//go:embed escalation.md
var escalation string

//go:embed chunk_summarizer.md
var chunkSummarizerRaw string

//go:embed conversation_summary_preamble.md
var conversationSummaryPreamble string

//go:embed steering_message.md
var steeringMessageRaw string

//go:embed environment_header.md
var environmentHeaderRaw string

// SummaryTemplate returns the anchored summary prompt template used during
// context compaction. It instructs the model to produce a structured
// Markdown summary with fixed sections.
func SummaryTemplate() string {
	return summaryTemplate
}

// ContractRules returns the Contract Rules preamble injected into
// sub-agent system prompts. It defines the evidence/interpretation
// field contract that sub-agents must follow.
func ContractRules() string {
	return contractRules
}

// Escalation returns the escalation format block injected into
// sub-agent system prompts. It defines the fenced escalation block
// and severity levels for structured error reporting.
func Escalation() string {
	return escalation
}

// --- Chunk summarizer helpers ---

// chunkSummarizerSections holds the four prompt sections split from
// chunk_summarizer.md by the "---" separator.
var chunkSummarizerSections []string

func init() {
	chunkSummarizerSections = strings.Split(strings.TrimSpace(chunkSummarizerRaw), "\n---\n")
	if len(chunkSummarizerSections) != 4 {
		panic(fmt.Sprintf("chunk_summarizer.md: expected 4 sections, got %d", len(chunkSummarizerSections)))
	}
}

// ChunkPreamble returns the "Summarize chunk X/Y…" prompt with the
// given index and total substituted for {{NUM}} and {{TOTAL}}.
func ChunkPreamble(num, total int) string {
	s := chunkSummarizerSections[0]
	s = strings.ReplaceAll(s, "{{NUM}}", fmt.Sprint(num))
	s = strings.ReplaceAll(s, "{{TOTAL}}", fmt.Sprint(total))
	return s
}

// ChunkSummarizerRole returns the system role for per-chunk
// summarization.
func ChunkSummarizerRole() string {
	return chunkSummarizerSections[1]
}

// ChunkMergerPreamble returns the preamble for merging partial
// summaries into one coherent summary.
func ChunkMergerPreamble() string {
	return chunkSummarizerSections[2]
}

// ChunkMergerRole returns the system role for meta-summarization
// (merging partial summaries).
func ChunkMergerRole() string {
	return chunkSummarizerSections[3]
}

// --- Conversation summary ---

// ConversationSummaryPreamble returns the preamble for the
// conversation summarization prompt used during REPL compaction.
func ConversationSummaryPreamble() string {
	return conversationSummaryPreamble
}

// --- Steering message ---

// SteeringMessage returns the loop-detection steering prompt with
// {{TOOL}} and {{COUNT}} replaced by the given values.
func SteeringMessage(tool string, count int) string {
	s := steeringMessageRaw
	s = strings.ReplaceAll(s, "{{TOOL}}", fmt.Sprintf("%q", tool))
	s = strings.ReplaceAll(s, "{{COUNT}}", fmt.Sprint(count))
	return s
}

// --- Environment header ---

// EnvironmentHeader returns the sub-agent environment block with
// {{OS}}, {{ARCH}}, {{SHELL}}, and {{CWD}} replaced by the given values.
func EnvironmentHeader(os, arch, shell, cwd string) string {
	s := environmentHeaderRaw
	s = strings.ReplaceAll(s, "{{OS}}", os)
	s = strings.ReplaceAll(s, "{{ARCH}}", arch)
	s = strings.ReplaceAll(s, "{{SHELL}}", shell)
	s = strings.ReplaceAll(s, "{{CWD}}", cwd)
	return s
}

// BuiltinRolesFS embeds the built-in sub-agent role definitions under
// roles/*.md. Each file is a YAML frontmatter block + markdown body
// that together define a role's tool set, limits, and system guidance.
// Filesystem roles in ~/.agents/roles/ (or ./.agents/roles/) extend
// this set at runtime without recompilation.
//
//go:embed roles/*.md
var BuiltinRolesFS embed.FS

// Layers holds the composable pieces of the system prompt assembled
// from multiple sources.
type Layers struct {
	Identity               string // embedded identity (always present)
	Environment            string // runtime OS/arch/shell context
	UserContext            string // ~/.yaah/AGENTS.md (optional)
	Project                string // walked-up AGENTS.md/CLAUDE.md from cwd
	Memory                 string // stored facts from SQLite
	Skills                 string // formatted skill index (name + description)
	MaxSubAgentConcurrency int    // injected into prompt so the model knows the semaphore limit
}

// Build assembles the full system prompt from the given layers by
// joining non-empty components with double-newline separators.
func Build(l Layers) string {
	var parts []string

	if strings.TrimSpace(l.Identity) != "" {
		parts = append(parts, l.Identity)
	}

	if l.MaxSubAgentConcurrency > 0 {
		parts = append(parts, fmt.Sprintf(
			"Your sub-agent concurrency limit is %d. You can dispatch up to %d spawn_subagent calls "+
				"per turn; additional calls queue behind the semaphore. Batch dispatches in waves of "+
				"%d or fewer for optimal throughput.",
			l.MaxSubAgentConcurrency, l.MaxSubAgentConcurrency, l.MaxSubAgentConcurrency,
		))
	}

	if strings.TrimSpace(l.Environment) != "" {
		parts = append(parts, "## Runtime Environment\n"+strings.TrimSpace(l.Environment))
	}

	if strings.TrimSpace(l.UserContext) != "" {
		parts = append(parts, "<user-preferences>\n"+strings.TrimSpace(l.UserContext)+"\n</user-preferences>")
	}

	if strings.TrimSpace(l.Project) != "" {
		parts = append(parts, l.Project)
	}

	if strings.TrimSpace(l.Memory) != "" {
		parts = append(parts, "## Memory\n"+strings.TrimSpace(l.Memory))
	}

	if strings.TrimSpace(l.Skills) != "" {
		parts = append(parts, l.Skills)
	}

	return strings.Join(parts, "\n\n")
}

// skillDescMaxChars caps each skill description inlined into the always-on
// index. Full descriptions remain available via the on-demand skill tool.
// Without a cap, a large skill set (200+) inlines tens of thousands of tokens
// into the system prompt every turn — most of it wasted, since a session
// typically loads 0–2 skills.
const skillDescMaxChars = 120

// BuildSkillsIndex returns a formatted "## Available Skills" section with the
// name plus a one-line description per skill. Descriptions are capped (see
// truncateSkillDesc); the full text is fetched on demand via the skill tool.
// Returns "" if no skills found.
func BuildSkillsIndex(skillList []skills.Skill) string {
	if len(skillList) == 0 {
		return ""
	}
	sorted := make([]skills.Skill, len(skillList))
	copy(sorted, skillList)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	var sb strings.Builder
	sb.WriteString("## Available Skills\n")
	for _, s := range sorted {
		desc := truncateSkillDesc(s.Description, s.Name)
		sb.WriteString(fmt.Sprintf("- **%s**: %s\n", s.Name, desc))
	}
	return sb.String()
}

// truncateSkillDesc reduces a skill description to a single line of at most
// skillDescMaxChars runes, breaking at a word boundary and appending an
// ellipsis when shortened. It is rune-aware so multibyte characters (em-dashes,
// etc.) are never split. The fallback is returned when desc is empty.
func truncateSkillDesc(desc, fallback string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return fallback
	}
	if i := strings.IndexByte(desc, '\n'); i >= 0 {
		desc = strings.TrimSpace(desc[:i]) // first line only
	}
	r := []rune(desc)
	if len(r) <= skillDescMaxChars {
		return desc
	}
	cut := skillDescMaxChars
	for cut > 0 && r[cut-1] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = skillDescMaxChars // no space within range; hard cut
	}
	return strings.TrimRight(string(r[:cut]), " ,;:-—") + "…"
}

// DetectEnvironment returns a human-readable string describing the
// current OS, architecture, default shell, and working directory.
func DetectEnvironment(cwd string) string {
	shell := "bash"
	if runtime.GOOS == "windows" {
		shell = "powershell (pwsh 7+ or Windows PowerShell)"
	}
	return fmt.Sprintf("OS: %s/%s. Default shell: %s. Working directory: %s.", runtime.GOOS, runtime.GOARCH, shell, cwd)
}

// LoadUserContext reads the user-level AGENTS.md from the yaah home
// directory. Returns empty string if the file doesn't exist.
func LoadUserContext(homeDir string) string {
	data, err := instructions.ReadFile(homeDir)
	if err != nil {
		return ""
	}
	return string(data)
}
