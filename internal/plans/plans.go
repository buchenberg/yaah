// Package plans discovers and parses PLAN.md files from the standard
// locations: project (.agents/plans/) and user-level (~/.agents/plans/).
// Plans use the same YAML frontmatter + markdown body convention as skills,
// with an additional "status" field for workflow tracking.
package plans

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
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
	frontmatter, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}

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
			plan.Name = unquote(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "description:") {
			plan.Description = unquote(strings.TrimPrefix(line, "description:"))
		} else if strings.HasPrefix(line, "status:") {
			plan.Status = unquote(strings.TrimPrefix(line, "status:"))
		}
	}

	if plan.Name == "" {
		return nil, fmt.Errorf("frontmatter missing 'name' field")
	}

	return plan, nil
}

// unquote strips a matching pair of YAML quotes from a scalar. The
// frontmatter reader is line-based so it does not decode YAML escapes; it
// only needs to keep quoted descriptions from rendering with their quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
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

// validatePlanName rejects names that could escape the plans root. The name
// becomes a directory beneath it, so it must be a single path segment.
func validatePlanName(name string) error {
	if name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) ||
		strings.TrimRight(name, " .") != name ||
		filepath.IsAbs(name) ||
		filepath.Base(name) != name {
		return fmt.Errorf("invalid plan name %q: use a single path segment without separators or trailing spaces/dots", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid plan name %q: control characters are not allowed", name)
		}
	}
	return nil
}

// Create writes a new PLAN.md file in the given directory. The plan is
// placed at <dir>/<name>/PLAN.md. Returns the path to the created file.
func Create(dir, name, description, body string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("plan name is required")
	}
	if err := validatePlanName(name); err != nil {
		return "", err
	}
	planDir := filepath.Join(dir, name)
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		return "", fmt.Errorf("create plan directory: %w", err)
	}
	path := filepath.Join(planDir, "PLAN.md")
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\nstatus: draft\n---\n\n%s\n", name, description, body)
	// O_EXCL makes "does it exist?" and "create it" one atomic operation. The
	// previous Stat-then-WriteFile raced: two concurrent creates both saw the
	// plan as absent and one silently overwrote the other's PLAN.md.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("plan %q already exists at %s", name, path)
		}
		return "", fmt.Errorf("write PLAN.md: %w", err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		// The O_EXCL create already claimed the name, so leaving the
		// partial file behind would wedge it forever: every later create
		// would report "already exists" for a plan that has no usable
		// content.
		os.Remove(path)
		return "", fmt.Errorf("write PLAN.md: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("write PLAN.md: %w", err)
	}
	return path, nil
}

// splitFrontmatter returns the raw YAML block between the fences and the body
// after them. Both callers need the text, not just the parsed struct: Edit
// rewrites the block in place so it can keep what it does not model.
func splitFrontmatter(data []byte) (frontmatter, body string, err error) {
	s := string(data)
	if !strings.HasPrefix(s, "---") {
		return "", "", fmt.Errorf("missing YAML frontmatter")
	}
	rest := s[3:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return "", "", fmt.Errorf("unclosed YAML frontmatter")
	}
	return rest[:endIdx], rest[endIdx+4:], nil
}

// rewriteFrontmatter returns the frontmatter block with the modelled keys set,
// preserving every other line verbatim.
//
// Edit used to rebuild the block from name/description/status alone, so any key
// this package does not model — owner, depends_on, a link to the issue the plan
// came from — was silently deleted on every status change, with no error and no
// way to notice until someone went looking for it. Rewriting line by line keeps
// unknown keys, their order, and comments.
//
// Keys are matched only at column zero, so a nested key (or an indented
// continuation line) that happens to start with "name:" is left alone. This
// package's parser reads the modelled keys as single-line "key: value" pairs, so
// a multi-line modelled value was never supported and is not reinterpreted here.
func rewriteFrontmatter(frontmatter string, updates map[string]string, order []string) string {
	seen := make(map[string]bool, len(updates))
	lines := strings.Split(strings.TrimSpace(frontmatter), "\n")
	out := make([]string, 0, len(lines)+len(updates))
	for _, raw := range lines {
		// Normalise the line ending first: on a CRLF file the trailing "\r"
		// would defeat the column-zero test and the key would be appended as
		// a duplicate rather than replaced.
		line := strings.TrimSuffix(raw, "\r")
		replaced := false
		// A top-level key is one with no leading indentation.
		if line == strings.TrimLeft(line, " \t") {
			for _, key := range order {
				val, ok := updates[key]
				if !ok {
					continue
				}
				rest, found := strings.CutPrefix(line, key+":")
				if !found {
					continue
				}
				out = append(out, key+": "+restyleValue(rest, val))
				seen[key] = true
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, line)
		}
	}
	for _, key := range order {
		if val, ok := updates[key]; ok && !seen[key] {
			out = append(out, key+": "+val)
		}
	}
	return strings.Join(out, "\n")
}

// restyleValue rebuilds "key:<rest>" after replacing the value with val,
// keeping the two things a naive rewrite would silently drop: a trailing
// inline comment, and the original quoting when it was a simple quoted scalar.
func restyleValue(rest, val string) string {
	comment := ""
	if i := strings.Index(rest, " #"); i >= 0 {
		comment = rest[i:]
		rest = rest[:i]
	}
	old := strings.TrimSpace(rest)
	if len(old) >= 2 && (old[0] == '"' || old[0] == '\'') && old[len(old)-1] == old[0] {
		val = string(old[0]) + val + string(old[0])
	}
	return val + comment
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

	frontmatter, _, err := splitFrontmatter(data)
	if err != nil {
		return "", fmt.Errorf("parse PLAN.md: %w", err)
	}
	rewritten := rewriteFrontmatter(frontmatter, map[string]string{
		"name":        current.Name,
		"description": description,
		"status":      status,
	}, []string{"name", "description", "status"})
	content := "---\n" + rewritten + "\n---\n\n" + body + "\n"
	// Preserve the mode the file already had: replacing it with a temp file
	// would otherwise silently reset a 0600 plan to 0644.
	perm := os.FileMode(0o644)
	if fi, statErr := os.Stat(plan.Path); statErr == nil {
		perm = fi.Mode().Perm()
	}
	if err := writeFileAtomic(plan.Path, []byte(content), perm); err != nil {
		return "", fmt.Errorf("write PLAN.md: %w", err)
	}
	return plan.Path, nil
}

// writeFileAtomic replaces path by writing a temp file beside it and renaming
// over the target. An interrupted Edit used to truncate PLAN.md in place, and
// the result is not merely a stale plan: a file whose frontmatter no longer
// parses is skipped by discovery, so the plan disappeared with no diagnostic.
// The rename is atomic on POSIX and on Windows via os.Rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".PLAN-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename below succeeds
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// Sync before the rename so a crash cannot leave the renamed file
	// present but empty; without it the atomicity is only against
	// interruptions, not power loss.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
