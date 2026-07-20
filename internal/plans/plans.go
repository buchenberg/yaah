// Package plans discovers and parses PLAN.md files from the standard
// locations: project (.agents/plans/) and user-level (~/.agents/plans/).
// Plans use the same YAML frontmatter + markdown body convention as skills,
// with an additional "status" field for workflow tracking.
package plans

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Plan represents a discovered plan with its metadata and body.
type Plan struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Status      string `yaml:"status" json:"status"`
	Body        string `yaml:"-" json:"body"`
	Path        string `yaml:"-" json:"path"` // path to the PLAN.md file
	Dir         string `yaml:"-" json:"dir"`  // directory containing the plan
}

// ValidStatuses are the recognised plan statuses.
var ValidStatuses = []string{"draft", "approved", "in_progress", "completed", "cancelled"}

// Discover scans the given directories for plans. Each directory should
// contain subdirectories, each with a PLAN.md file. Directories are
// scanned in order; the first directory with a plan of a given name wins.
func Discover(dirs []string) []Plan {
	seen := make(map[string]bool)
	var out []Plan

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
			planMD := filepath.Join(dir, entry.Name(), "PLAN.md")
			data, err := os.ReadFile(planMD)
			if err != nil {
				continue
			}

			plan, err := ParseFrontmatter(data)
			if err != nil {
				continue
			}

			if seen[plan.Name] {
				continue
			}
			seen[plan.Name] = true

			plan.Path = planMD
			plan.Dir = filepath.Dir(planMD)
			out = append(out, *plan)
		}
	}

	return out
}

// ParseFrontmatter parses YAML frontmatter (between --- markers) and the
// markdown body from a PLAN.md file.
func ParseFrontmatter(data []byte) (*Plan, error) {
	s := string(data)

	if !strings.HasPrefix(s, "---") {
		return nil, fmt.Errorf("missing YAML frontmatter")
	}

	rest := s[3:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return nil, fmt.Errorf("unclosed YAML frontmatter")
	}

	frontmatter := rest[:endIdx]
	body := rest[endIdx+4:]

	plan := &Plan{
		Body:   strings.TrimSpace(body),
		Status: "draft",
	}

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "name:") {
			plan.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "description:") {
			plan.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		} else if strings.HasPrefix(line, "status:") {
			plan.Status = strings.TrimSpace(strings.TrimPrefix(line, "status:"))
		}
	}

	if plan.Name == "" {
		return nil, fmt.Errorf("frontmatter missing 'name' field")
	}

	return plan, nil
}

// FindPlan discovers plans from the given dirs and returns the one with
// the matching name, or nil if not found.
func FindPlan(dirs []string, name string) *Plan {
	for _, p := range Discover(dirs) {
		if p.Name == name {
			return &p
		}
	}
	return nil
}

// FormatPlanForAgent returns the plan content in a format suitable for
// injecting into the agent context.
func FormatPlanForAgent(plan *Plan) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "<plan_content name=%q status=%q>\n", plan.Name, plan.Status)
	fmt.Fprintf(&buf, "# Plan: %s\n", plan.Name)
	fmt.Fprintf(&buf, "Status: %s\n\n", plan.Status)
	fmt.Fprintf(&buf, "%s\n\n", plan.Body)
	fmt.Fprintf(&buf, "Plan directory: %s\n", plan.Dir)
	buf.WriteString("</plan_content>\n")
	return buf.String()
}

// Create writes a new PLAN.md file in the given directory. The plan is
// placed at <dir>/<name>/PLAN.md. Returns the path to the created file.
func Create(dir, name, description, body string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("plan name is required")
	}
	planDir := filepath.Join(dir, name)
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		return "", fmt.Errorf("create plan directory: %w", err)
	}
	path := filepath.Join(planDir, "PLAN.md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("plan %q already exists at %s", name, path)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\nstatus: draft\n---\n\n%s\n", name, description, body)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write PLAN.md: %w", err)
	}
	return path, nil
}

// Edit updates an existing PLAN.md file. Non-empty fields overwrite the
// corresponding frontmatter or body. Returns the path to the edited file.
func Edit(plan *Plan, description, status, body string) (string, error) {
	data, err := os.ReadFile(plan.Path)
	if err != nil {
		return "", fmt.Errorf("read PLAN.md: %w", err)
	}

	current, err := ParseFrontmatter(data)
	if err != nil {
		return "", fmt.Errorf("parse PLAN.md: %w", err)
	}

	if description == "" {
		description = current.Description
	}
	if status == "" {
		status = current.Status
	}
	if body == "" {
		body = current.Body
	}

	content := fmt.Sprintf("---\nname: %s\ndescription: %s\nstatus: %s\n---\n\n%s\n", current.Name, description, status, body)
	if err := os.WriteFile(plan.Path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write PLAN.md: %w", err)
	}
	return plan.Path, nil
}

// SetStatus updates only the status field of a plan's frontmatter.
func SetStatus(plan *Plan, status string) (string, error) {
	return Edit(plan, "", status, "")
}

// Delete removes the plan directory and its contents.
func Delete(plan *Plan) error {
	if plan.Dir == "" {
		return fmt.Errorf("plan has no directory path")
	}
	return os.RemoveAll(plan.Dir)
}
