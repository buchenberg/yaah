// Package prompts provides the embedded system prompt identity and
// assembly functions for constructing the full system prompt from
// multiple layers: embedded identity, user context, project context,
// memory, and skills.
package prompts

import (
	_ "embed"
	"strings"

	"github.com/buchenberg/yaah/internal/instructions"
)

// IdentityPrompt is the default system identity shipped with the yaah
// binary. It defines what yaah is, its principles, capabilities, and
// behavioral rules.
//
//go:embed identity.md
var IdentityPrompt string

// Layers holds the composable pieces of the system prompt assembled
// from multiple sources.
type Layers struct {
	Identity    string // embedded identity (always present)
	UserContext string // ~/.yaah/AGENTS.md (optional)
	Project     string // walked-up AGENTS.md/CLAUDE.md from cwd
	Memory      string // stored facts from SQLite
}

// Build assembles the full system prompt from the given layers by
// joining non-empty components with double-newline separators.
func Build(l Layers) string {
	var parts []string

	if strings.TrimSpace(l.Identity) != "" {
		parts = append(parts, l.Identity)
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

	return strings.Join(parts, "\n\n")
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
