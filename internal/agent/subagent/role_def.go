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
// (minus extension) is the role name.
type RoleDef struct {
	Tools         []string `yaml:"tools"`
	MaxIterations int      `yaml:"max_iterations"`
	Timeout       int      `yaml:"timeout"` // seconds; 0 = no timeout
	MaxDepth      int      `yaml:"max_depth"`

	// Body is the markdown content after the YAML frontmatter block.
	// It is injected as role guidance into the sub-agent's system
	// prompt so the sub-agent understands its constraints.
	Body string `yaml:"-"`
}

// ToProfile converts a parsed role definition into the runtime
// RoleProfile used by the agent loop and sub-agent wiring.
func (d RoleDef) ToProfile() RoleProfile {
	p := RoleProfile{
		Tools:         d.Tools,
		MaxIterations: d.MaxIterations,
		Timeout:       time.Duration(d.Timeout) * time.Second,
		MaxDepth:      d.MaxDepth,
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

// ProfileFor returns the runtime profile for the given role. The
// RoleDefault role (empty string) returns the zero-value profile,
// signalling the caller to use the legacy full tool set. Unknown
// roles also return the zero value.
func (r *RoleRegistry) ProfileFor(role SubAgentRole) RoleProfile {
	if role == RoleDefault {
		return RoleProfile{}
	}
	r.mu.RLock()
	def, ok := r.entries[role]
	r.mu.RUnlock()
	if !ok {
		return RoleProfile{}
	}
	return def.ToProfile()
}

// Guidance returns the role-specific system-prompt text for the given
// role. Returns "" for RoleDefault or unknown roles.
func (r *RoleRegistry) Guidance(role SubAgentRole) string {
	if role == RoleDefault {
		return ""
	}
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
