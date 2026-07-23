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

// BuildSkillsIndex returns a formatted "## Available Skills" section
// with name + description per skill. Returns "" if no skills found.
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
		desc := s.Description
		if desc == "" {
			desc = s.Name
		}
		sb.WriteString(fmt.Sprintf("- **%s**: %s\n", s.Name, desc))
	}
	return sb.String()
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
