// Package instructions discovers and loads project instruction files
// (AGENTS.md, CLAUDE.md, CONTEXT.md) by walking up from the current
// working directory to the worktree root. This matches the emerging
// standard used by opencode, Claude Code, and Hermes Agent.
package instructions

import (
	"os"
	"path/filepath"
	"strings"
)

// instructionFileNames are the files we look for, in priority order.
// AGENTS.md is preferred; CLAUDE.md is accepted for cross-tool compatibility.
var instructionFileNames = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"CONTEXT.md", // deprecated but still accepted
}

// ReadFile reads a single instruction file (AGENTS.md, CLAUDE.md, or
// CONTEXT.md) from the given directory. Returns the file contents or an
// error. Only the first matching file is returned (AGENTS.md preferred).
func ReadFile(dir string) ([]byte, error) {
	for _, name := range instructionFileNames {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
	}
	return nil, os.ErrNotExist
}

// Load discovers instruction files by walking from cwd up to worktreeRoot.
// It returns the contents of each file found, in discovery order (closest
// ancestor first). AGENTS.md takes priority over CLAUDE.md at each level.
func Load(cwd, worktreeRoot string) []string {
	var files []string
	seen := make(map[string]bool)

	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, name := range instructionFileNames {
			path := filepath.Join(dir, name)
			if seen[path] {
				continue
			}

			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			seen[path] = true
			files = append(files, string(data))
		}

		// Stop at the worktree root
		if dir == worktreeRoot {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached filesystem root
		}
	}

	return files
}

// FormatForSystem returns the instruction files formatted for injection
// into the system prompt.
func FormatForSystem(files []string) string {
	if len(files) == 0 {
		return ""
	}

	var buf strings.Builder
	buf.WriteString("<project-instructions>\n\n")
	for i, content := range files {
		if i > 0 {
			buf.WriteString("\n---\n\n")
		}
		buf.WriteString(strings.TrimSpace(content))
		buf.WriteString("\n")
	}
	buf.WriteString("</project-instructions>")
	return buf.String()
}
