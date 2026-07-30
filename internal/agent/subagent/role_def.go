package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// RoleDef is the persistent format for a sub-agent role definition,
// loaded from a markdown file with YAML frontmatter. The file name
// (minus extension) is the role identifier.

// ContractField names a field in a sub-agent's response contract and
// optionally classifies it as evidence (raw tool output, verifiable) or
// interpretation (model synthesis, may need verification).
type ContractField struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"` // "evidence" or "interpretation"; empty = interpretation
}

// UnmarshalYAML accepts both string and map forms so existing role YAMLs
// remain valid:
//   - "fieldname"            → ContractField{Name: "fieldname"}
//   - {name: field, kind: e} → ContractField{Name: "field", Kind: "e"}
func (f *ContractField) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		f.Name = value.Value
		return nil
	case yaml.MappingNode:
		type raw ContractField
		return value.Decode((*raw)(f))
	default:
		return fmt.Errorf("contract field must be a string or map, got %v", value.Tag)
	}
}

// ContractDef describes the structured output block a sub-agent must
// append to its response so the main agent can extract data reliably.
type ContractDef struct {
	Heading string          `yaml:"heading"`
	Fields  []ContractField `yaml:"fields"`
}

type RoleDef struct {
	DisplayName string `yaml:"name"`
	Specialty   string `yaml:"specialty"`
	// Description is a one-line summary shown to the orchestrator so
	// it can choose the right role without calling list_subagents.
	Description   string      `yaml:"description"`
	Contract      ContractDef `yaml:"contract"`
	Tools         []string    `yaml:"tools"`
	MaxIterations int         `yaml:"max_iterations"`
	MaxTurns      int         `yaml:"max_turns"`
	JSONMode      bool        `yaml:"json_mode"`
	Timeout       int         `yaml:"timeout"` // seconds; 0 = no timeout

	Body string `yaml:"-"`
}

// ToProfile converts a parsed role definition into the runtime
// RoleProfile used by the agent loop and sub-agent wiring.
func (d RoleDef) ToProfile() RoleProfile {
	p := RoleProfile{
		DisplayName:   d.DisplayName,
		Specialty:     d.Specialty,
		Description:   d.Description,
		Contract:      d.Contract,
		Tools:         d.Tools,
		MaxIterations: d.MaxIterations,
		MaxTurns:      d.MaxTurns,
		JSONMode:      d.JSONMode,
		Timeout:       time.Duration(d.Timeout) * time.Second,
	}
	if p.Tools == nil {
		p.Tools = []string{}
	}
	return p
}

// RoleRegistry holds discovered sub-agent role definitions: the
// built-in set embedded in the binary, and any user-defined roles
// found on the filesystem. Built-in roles take precedence.
type RoleRegistry struct {
	mu      sync.RWMutex
	entries map[SubAgentRole]RoleDef
}

// NewRoleRegistry returns an empty registry ready for loading.
func NewRoleRegistry() *RoleRegistry {
	return &RoleRegistry{entries: make(map[SubAgentRole]RoleDef)}
}

// LoadBytes parses role definitions from the given byte slices, keyed
// by file name (e.g. "worker.md"). The file name minus its extension
// becomes the role name. Roles loaded via this method are marked as
// built-in; later calls to LoadDir will not overwrite them.
func (r *RoleRegistry) LoadBytes(files map[string][]byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, data := range files {
		roleName := strings.TrimSuffix(name, ".md")
		if roleName == "" || roleName == name {
			continue
		}
		def, err := parseRoleFile(data)
		if err != nil {
			return fmt.Errorf("role %q: %w", roleName, err)
		}
		r.entries[SubAgentRole(roleName)] = def
	}
	return nil
}

// LoadDir walks dir and parses every .md file as a role definition.
// Roles already present in the registry (i.e. built-in) are NOT
// overwritten, so user-defined roles cannot shadow built-in roles.
// Non-.md files and directories are skipped silently.
func (r *RoleRegistry) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		roleName := strings.TrimSuffix(e.Name(), ".md")
		key := SubAgentRole(roleName)
		if _, exists := r.entries[key]; exists {
			continue // built-in has precedence
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("role %q: %w", path, err)
		}
		def, err := parseRoleFile(data)
		if err != nil {
			return fmt.Errorf("role %q: %w", path, err)
		}
		r.entries[key] = def
	}
	return nil
}

// ProfileFor returns the runtime profile for the given role. Unknown
// roles return the zero-value profile, which callers treat as a
// configuration error (no tools, no limits).
func (r *RoleRegistry) ProfileFor(role SubAgentRole) RoleProfile {
	r.mu.RLock()
	def, ok := r.entries[role]
	r.mu.RUnlock()
	if !ok {
		return RoleProfile{}
	}
	return def.ToProfile()
}

// Guidance returns the role-specific system-prompt text for the given
// role. Returns "" for unknown roles.
func (r *RoleRegistry) Guidance(role SubAgentRole) string {
	r.mu.RLock()
	def, ok := r.entries[role]
	r.mu.RUnlock()
	if !ok {
		return ""
	}
	return def.Body
}

// Names returns all known role names in insertion order (built-in
// first, then user-defined), suitable for building the task tool's
// JSON schema enum.
func (r *RoleRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, string(name))
	}
	return names
}

// List returns all registered role definitions so external code can
// enumerate role capabilities (names, descriptions, tool sets).
func (r *RoleRegistry) List() map[SubAgentRole]RoleDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[SubAgentRole]RoleDef, len(r.entries))
	for name, def := range r.entries {
		out[name] = def
	}
	return out
}

// parseRoleFile splits raw markdown content at the first YAML
// frontmatter block (delimited by "---" lines) and unmarshals the
// YAML portion into a RoleDef. The remaining markdown is stored in
// the Body field.
func parseRoleFile(data []byte) (RoleDef, error) {
	text := string(data)
	var def RoleDef

	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "---") {
		return def, fmt.Errorf("missing YAML frontmatter (file must start with ---)")
	}

	text = text[3:] // strip opening ---
	idx := strings.Index(text, "\n---")
	if idx < 0 {
		// Single-line: "---\nkey: value\n---\nbody..."
		idx = strings.Index(text, "\n---\n")
	}
	if idx < 0 {
		return def, fmt.Errorf("unterminated YAML frontmatter (missing closing ---)")
	}

	yamlBlock := text[:idx]
	body := strings.TrimSpace(text[idx+4:]) // strip "\n---" + newline

	if err := yaml.Unmarshal([]byte(yamlBlock), &def); err != nil {
		return def, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	def.Body = body
	return def, nil
}
