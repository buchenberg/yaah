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
			"specialty": {"type": "string", "description": "Short specialty label (optional)"},
			"mode": {"type": "string", "enum": ["primary", "subagent", "all"], "description": "Capability mode: primary = orchestrator-eligible, subagent = delegate-only, all = both; omitted means all (the file-format default) and the key is omitted from the file. edit: omitted means unchanged; there is no unset via this tool — an explicit \"subagent\" restores the delegate-only capability but keeps the key; removing the key restores the all default and is a direct file edit, not a tool operation."}
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
		Mode        string `json:"mode"`
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
		if err := validateRoleMode(params.Mode); err != nil {
			return "", err
		}
		return t.createRole(roleParams{
			Name:        params.Name,
			Description: params.Description,
			Body:        params.Body,
			Tools:       params.Tools,
			Specialty:   params.Specialty,
			Mode:        params.Mode,
		})
	case "edit":
		if params.Name == "" {
			return "", fmt.Errorf("role 'edit' requires a 'name'")
		}
		if err := validateRoleMode(params.Mode); err != nil {
			return "", err
		}
		return t.editRole(roleParams{
			Name:        params.Name,
			Description: params.Description,
			Body:        params.Body,
			Tools:       params.Tools,
			Specialty:   params.Specialty,
			Mode:        params.Mode,
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
	// Build name→description and name→mode maps for sorted output.
	desc := make(map[string]string, len(roles))
	modes := make(map[string]string, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
		desc[r.Name] = r.Description
		modes[r.Name] = r.Mode
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "- **%s**%s: %s\n", name, roleModeAnnotation(modes[name]), desc[name])
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
	Mode        string
}

// createRole writes a new role .md file and reloads the registry.
func (t *RoleTool) createRole(p roleParams) (string, error) {
	dir := t.writeDir()
	// Resolve before creating anything: a rejected name must leave the host
	// untouched, so it cannot even make the roles directory.
	path, err := roleFilePath(dir, p.Name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating role directory %s: %w", dir, err)
	}

	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("role %q already exists at %s (use 'edit' to modify)", p.Name, path)
	}

	fm := rolefile.Frontmatter{
		Name:        p.Name,
		Description: p.Description,
		Specialty:   p.Specialty,
		// Marshal emits keys in struct field order, so a provided mode lands
		// with the other create-written keys (after tools, before the budget
		// block); empty omits the key entirely (omitempty).
		Mode: p.Mode,
	}
	fm.Tools = parseToolsList(p.Tools)

	content, err := rolefile.Marshal(fm, p.Body)
	if err != nil {
		return "", fmt.Errorf("marshaling role: %w", err)
	}

	if err := atomicWriteFile(path, []byte(content), 0o644); err != nil {
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

	if p.Description == "" && p.Body == "" && p.Specialty == "" && p.Tools == "" && p.Mode == "" {
		return "nothing to edit — no fields provided", nil
	}
	if p.Description != "" {
		existing.Description = p.Description
	}
	if p.Specialty != "" {
		existing.Specialty = p.Specialty
	}
	// Mirror description/specialty: empty means unchanged, non-empty
	// replaces. There is no unset — passing "subagent" restores the
	// delegate-only capability but keeps the key; removing the key is a
	// file edit that restores the file-format default ("all").
	if p.Mode != "" {
		existing.Mode = p.Mode
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

	if err := atomicWriteFile(path, []byte(content), 0o644); err != nil {
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
	// Validate once, up front, so an invalid name is reported as invalid
	// rather than as "not found" — and so the loop below never builds a path
	// from it, even when there are no search directories.
	if err := validateRoleName(name); err != nil {
		return "", err
	}
	for _, dir := range t.Dirs {
		path, err := roleFilePath(dir, name)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("%w: not found in any search directory", RoleNotFoundError{Role: name})
}

// validateRoleName rejects names that are paths rather than role names. A role
// file is <dir>/<name>.md, so the name contributes exactly one path element:
// separators, traversal, absolute paths and volume names are refused before any
// path is built (bead yaah-06u).
func validateRoleName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("invalid role name: name is empty")
	case name == "." || name == "..":
		return fmt.Errorf("invalid role name %q: must name a role, not a directory", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("invalid role name %q: must not contain a path separator", name)
	case strings.ContainsRune(name, 0):
		return fmt.Errorf("invalid role name %q: must not contain a NUL byte", name)
	case filepath.IsAbs(name), filepath.VolumeName(name) != "", strings.HasPrefix(name, "~"):
		return fmt.Errorf("invalid role name %q: must be a bare name, not a path", name)
	}
	return nil
}

// defaultRoleMode is the capability every role file without a mode key
// resolves to. It mirrors subagent.normalizeRoleMode's default; the tools
// package cannot import agent/subagent, so the value is restated here.
const defaultRoleMode = "all"

// validateRoleMode enforces the tool-call vocabulary for the mode field
// (yaah-21uk.13). Unlike the registry's file-load path — which warns and
// falls back so a broken project role cannot brick startup — this is a
// direct tool call: reject the bad value so no role file is written with
// a mode that would silently downgrade to the default.
func validateRoleMode(mode string) error {
	switch mode {
	case "", "primary", "subagent", "all":
		return nil
	}
	return fmt.Errorf("invalid mode %q: must be \"primary\", \"subagent\" or \"all\"", mode)
}

// roleModeAnnotation renders the capability tag shown after a role name in
// list output. Only non-default capabilities are annotated: "all" — the
// file-format default — and an unset mode render plain; "primary" and
// "subagent" (explicit or the fail-closed fallback) are annotated, so
// files that named subagent explicitly gain a tag they did not have
// before the default flipped.
func roleModeAnnotation(mode string) string {
	if mode == "" || mode == defaultRoleMode {
		return ""
	}
	return " [" + mode + "]"
}

// roleFilePath joins a search directory and a validated role name into the
// role's file path, refusing any result that does not stay inside dir.
//
// validateRoleName already rejects separators, traversal and absolute names, so
// this second check is defense in depth: it holds even for a caller that builds
// the name from something other than the schema field, and it is what makes the
// containment claim true of the path actually opened or removed.
func roleFilePath(dir, name string) (string, error) {
	if err := validateRoleName(name); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".md")
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return "", fmt.Errorf("invalid role name %q: %w", name, err)
	}
	if rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid role name %q: resolves outside %s", name, dir)
	}
	return path, nil
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
