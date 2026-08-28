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

	"gopkg.in/yaml.v3"
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
// is missing or unparseable. The block is decoded with yaml.v3 — the
// line-by-line parser it replaces broke on quoted or wrapped values
// (review A5).
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

	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	skill.Name = strings.TrimSpace(fm.Name)
	skill.Description = strings.TrimSpace(fm.Description)

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

// Create writes a new SKILL.md file in the given directory. The skill is
// placed at <dir>/<name>/SKILL.md. Returns the path to the created file.
func Create(dir, name, description, body string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("skill name is required")
	}
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return "", fmt.Errorf("create skill directory: %w", err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("skill %q already exists at %s", name, path)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", name, description, body)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write SKILL.md: %w", err)
	}
	return path, nil
}

// Edit updates an existing SKILL.md file. Non-empty fields overwrite the
// corresponding frontmatter or body. Returns the path to the edited file.
func Edit(skill *Skill, description, body string) (string, error) {
	data, err := os.ReadFile(skill.Path)
	if err != nil {
		return "", fmt.Errorf("read SKILL.md: %w", err)
	}

	// Parse current frontmatter to preserve fields not being updated.
	current, err := ParseFrontmatter(data)
	if err != nil {
		return "", fmt.Errorf("parse SKILL.md: %w", err)
	}

	if description == "" {
		description = current.Description
	}
	if body == "" {
		body = current.Body
	}

	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", current.Name, description, body)
	if err := os.WriteFile(skill.Path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write SKILL.md: %w", err)
	}
	return skill.Path, nil
}
