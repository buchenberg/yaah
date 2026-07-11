// Package skills discovers and parses SKILL.md files from the standard
// locations: project (.agents/skills/), yaah (~/.yaah/skills/), and
// user-level cross-tool (~/.agents/skills/). Skills follow the emerging
// cross-tool convention: YAML frontmatter with name/description, followed
// by markdown body content.
package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill represents a discovered skill with its metadata and body.
type Skill struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Body        string `yaml:"-" json:"body"`
	Path        string `yaml:"-" json:"path"` // path to the SKILL.md file
	Dir         string `yaml:"-" json:"dir"`  // directory containing the skill
}

// Discover scans the given directories for skills. Each directory should
// contain subdirectories, each with a SKILL.md file. Directories are
// scanned in order; the first directory with a skill of a given name wins.
// Later duplicates are silently skipped.
func Discover(dirs []string) []Skill {
	seen := make(map[string]bool)
	var out []Skill

	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			continue
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillMD := filepath.Join(dir, entry.Name(), "SKILL.md")
			data, err := os.ReadFile(skillMD)
			if err != nil {
				continue
			}

			skill, err := ParseFrontmatter(data)
			if err != nil {
				continue
			}

			if seen[skill.Name] {
				continue
			}
			seen[skill.Name] = true

			skill.Path = skillMD
			skill.Dir = filepath.Dir(skillMD)
			out = append(out, *skill)
		}
	}

	return out
}

// ParseFrontmatter parses YAML frontmatter (between --- markers) and the
// markdown body from a SKILL.md file. Returns an error if the frontmatter
// is missing or unparseable.
func ParseFrontmatter(data []byte) (*Skill, error) {
	s := string(data)

	// Must start with ---
	if !strings.HasPrefix(s, "---") {
		return nil, fmt.Errorf("missing YAML frontmatter")
	}

	// Find the closing ---
	rest := s[3:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return nil, fmt.Errorf("unclosed YAML frontmatter")
	}

	frontmatter := rest[:endIdx]
	body := rest[endIdx+4:] // skip past the closing \n---

	skill := &Skill{
		Body: strings.TrimSpace(body),
	}

	// Simple line-by-line parser (avoids yaml.v3 dependency for now)
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "name:") {
			skill.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "description:") {
			skill.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}

	if skill.Name == "" {
		return nil, fmt.Errorf("frontmatter missing 'name' field")
	}

	return skill, nil
}

// FindSkill discovers skills from the given dirs and returns the one with
// the matching name, or nil if not found.
func FindSkill(dirs []string, name string) *Skill {
	for _, s := range Discover(dirs) {
		if s.Name == name {
			return &s
		}
	}
	return nil
}

// FormatSkillForAgent returns the skill content in the format expected by
// the agent loop: <skill_content> with base dir and file listing.
func FormatSkillForAgent(skill *Skill) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "<skill_content name=%q>\n", skill.Name)
	fmt.Fprintf(&buf, "# Skill: %s\n\n", skill.Name)
	fmt.Fprintf(&buf, "%s\n\n", skill.Body)
	fmt.Fprintf(&buf, "Base directory for this skill: %s\n", skill.Dir)
	buf.WriteString("</skill_content>\n")
	return buf.String()
}
