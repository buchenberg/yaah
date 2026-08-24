package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/rolefile"
)

// RoleTool lets the agent list, show, create, edit, and delete sub-agent role
// definitions stored as .md files with YAML frontmatter in role search
// directories. After every mutation the default role registry is reloaded so
// list_subagents reflects the change immediately.
type RoleTool struct {
	Dirs         []string          // role search directories (project-first, then user)
	BuiltinFiles map[string][]byte // built-in role files passed to ReloadRoles
	Resolver     RoleResolver      // role registry access (injected by agent/runner)
}

func (t *RoleTool) Name() string        { return "role" }
func (t *RoleTool) Description() string { return prompts.ToolDescription("role") }

func (t *RoleTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["list", "show", "create", "edit", "delete"], "description": "Action to perform"},
			"name": {"type": "string", "description": "Role name (required for show, create, edit, delete)"},
			"description": {"type": "string", "description": "One-line role description (required for create, optional for edit)"},
			"body": {"type": "string", "description": "Role system prompt body in markdown (required for create, optional for edit)"},
			"tools": {"type": "string", "description": "JSON array or comma-separated tool names (optional)"},
			"specialty": {"type": "string", "description": "Short specialty label (optional)"}
		},
		"required": ["action"]
	}`)
}

func (t *RoleTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Action      string `json:"action"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
		Tools       string `json:"tools"`
		Specialty   string `json:"specialty"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("role: invalid arguments: %w", err)
	}

	switch params.Action {
	case "list", "":
		return t.listRoles()
	case "show":
		if params.Name == "" {
			return "", fmt.Errorf("role 'show' requires a 'name'")
		}
		return t.showRole(params.Name)
	case "create":
		if params.Name == "" {
			return "", fmt.Errorf("role 'create' requires a 'name'")
		}
		return t.createRole(roleParams{
			Name:        params.Name,
			Description: params.Description,
			Body:        params.Body,
			Tools:       params.Tools,
			Specialty:   params.Specialty,
		})
	case "edit":
		if params.Name == "" {
			return "", fmt.Errorf("role 'edit' requires a 'name'")
		}
		return t.editRole(roleParams{
			Name:        params.Name,
			Description: params.Description,
			Body:        params.Body,
			Tools:       params.Tools,
			Specialty:   params.Specialty,
		})
	case "delete":
		if params.Name == "" {
			return "", fmt.Errorf("role 'delete' requires a 'name'")
		}
		return t.deleteRole(params.Name)
	default:
		return "", fmt.Errorf("unknown action %q (try list, show, create, edit, delete)", params.Action)
	}
}

// listRoles returns all roles from the current default registry.
func (t *RoleTool) listRoles() (string, error) {
	if t.Resolver == nil {
		return "no role registry loaded", nil
	}
	roles := t.Resolver.ListRoles()
	if len(roles) == 0 {
		return "no roles defined", nil
	}
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	// Build a name→description map for sorted output.
	desc := make(map[string]string, len(roles))
	for _, r := range roles {
		desc[r.Name] = r.Description
	}
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "- **%s**: %s\n", name, desc[name])
	}
	return strings.TrimSpace(b.String()), nil
}

// showRole reads and returns the full content of a role file.
func (t *RoleTool) showRole(name string) (string, error) {
	path, err := t.findRoleFile(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return string(data), nil
}

type roleParams struct {
	Name        string
	Description string
	Body        string
	Tools       string
	Specialty   string
}

// createRole writes a new role .md file and reloads the registry.
func (t *RoleTool) createRole(p roleParams) (string, error) {
	dir := t.writeDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating role directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, p.Name+".md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("role %q already exists at %s (use 'edit' to modify)", p.Name, path)
	}

	fm := rolefile.Frontmatter{
		Name:        p.Name,
		Description: p.Description,
		Specialty:   p.Specialty,
	}
	fm.Tools = parseToolsList(p.Tools)

	content, err := rolefile.Marshal(fm, p.Body)
	if err != nil {
		return "", fmt.Errorf("marshaling role: %w", err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing role file: %w", err)
	}

	if err := t.reloadRegistry(); err != nil {
		return "", fmt.Errorf("role file written but reload failed: %w", err)
	}

	return fmt.Sprintf("created role %q at %s (%d bytes)", p.Name, path, len(content)), nil
}

// editRole modifies an existing role file in-place and reloads the registry.
func (t *RoleTool) editRole(p roleParams) (string, error) {
	path, err := t.findRoleFile(p.Name)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}

	existing, existingBody, err := rolefile.Parse(string(data))
	if err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}

	if p.Description == "" && p.Body == "" && p.Specialty == "" && p.Tools == "" {
		return "nothing to edit — no fields provided", nil
	}
	if p.Description != "" {
		existing.Description = p.Description
	}
	if p.Specialty != "" {
		existing.Specialty = p.Specialty
	}
	if p.Body != "" {
		existingBody = strings.TrimSpace(p.Body)
	}
	if p.Tools != "" {
		existing.Tools = parseToolsList(p.Tools)
	}

	content, err := rolefile.Marshal(existing, existingBody)
	if err != nil {
		return "", fmt.Errorf("marshaling role: %w", err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing role file: %w", err)
	}

	if err := t.reloadRegistry(); err != nil {
		return "", fmt.Errorf("role file edited but reload failed: %w", err)
	}

	return fmt.Sprintf("edited role %q at %s (%d bytes)", p.Name, path, len(content)), nil
}

// deleteRole removes a role file and reloads the registry.
func (t *RoleTool) deleteRole(name string) (string, error) {
	path, err := t.findRoleFile(name)
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("removing %s: %w", path, err)
	}
	if err := t.reloadRegistry(); err != nil {
		return "", fmt.Errorf("role file deleted but reload failed: %w", err)
	}
	return fmt.Sprintf("deleted role %q (removed %s)", name, path), nil
}

// findRoleFile locates a role file by name across all search directories.
func (t *RoleTool) findRoleFile(name string) (string, error) {
	for _, dir := range t.Dirs {
		path := filepath.Join(dir, name+".md")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("%w: not found in any search directory", RoleNotFoundError{Role: name})
}

// writeDir returns the directory where new roles should be created.
func (t *RoleTool) writeDir() string {
	if len(t.Dirs) > 0 {
		return t.Dirs[0]
	}
	return ".agents/roles"
}

// reloadRegistry calls the resolver to atomically swap in the latest
// role definitions from disk.
func (t *RoleTool) reloadRegistry() error {
	if t.Resolver == nil {
		return nil
	}
	return t.Resolver.ReloadRoles(t.BuiltinFiles, t.Dirs)
}

// parseToolsList parses a tools string that can be a JSON array or
// comma-separated list.
func parseToolsList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "[") {
		var arr []string
		if err := yaml.Unmarshal([]byte(s), &arr); err == nil && len(arr) > 0 {
			return arr
		}
	}
	var arr []string
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok != "" {
			arr = append(arr, tok)
		}
	}
	return arr
}
