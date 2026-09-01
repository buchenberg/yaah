package subagent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/rolefile"
)

// RoleDef is the persistent format for a sub-agent role definition,
// loaded from a markdown file with YAML frontmatter. The file name
// (minus extension) is the role identifier.

// ContractField and ContractDef live in rolefile — the single role-file
// format shared with the role tool. The aliases keep existing references
// compiling.
type (
	ContractField = rolefile.ContractField
	ContractDef   = rolefile.ContractDef
)

type RoleDef struct {
	DisplayName string
	Specialty   string
	// Description is a one-line summary shown to the orchestrator so
	// it can choose the right role without calling list_subagents.
	Description   string
	Contract      ContractDef
	Tools         []string
	MaxLoopCycles int
	MinLoopCycles int // per-call overrides may not go below; 0 = none
	MaxToolTurns  int
	MinToolTurns  int // per-call overrides may not go below; 0 = none
	JSONMode      bool
	Timeout       int // seconds; 0 = no timeout

	Body string
}

// roleDefFrom converts the shared rolefile frontmatter into the runtime
// RoleDef. It is the only Frontmatter→RoleDef mapping, so the registry
// and the role tool cannot parse the same file differently (review B3).
func roleDefFrom(fm rolefile.Frontmatter, body string) RoleDef {
	def := RoleDef{
		DisplayName:   fm.Name,
		Specialty:     fm.Specialty,
		Description:   fm.Description,
		Contract:      fm.Contract,
		Tools:         fm.Tools,
		MaxLoopCycles: fm.MaxLoopCycles,
		MinLoopCycles: fm.MinLoopCycles,
		MaxToolTurns:  fm.MaxToolTurns,
		MinToolTurns:  fm.MinToolTurns,
		JSONMode:      fm.JSONMode,
		Timeout:       fm.Timeout,
		Body:          body,
	}

	// Clamp invalid budget floors instead of failing: a broken project
	// role must not brick startup (plan §4.6). Config-level floors are
	// hard-validated at load; role files only warn.
	if def.MinLoopCycles < 0 {
		slog.Warn("role file: negative min_iterations clamped to 0", "role", def.DisplayName, "value", def.MinLoopCycles)
		def.MinLoopCycles = 0
	}
	if def.MinToolTurns < 0 {
		slog.Warn("role file: negative min_turns clamped to 0", "role", def.DisplayName, "value", def.MinToolTurns)
		def.MinToolTurns = 0
	}
	if def.MaxLoopCycles > 0 && def.MinLoopCycles > def.MaxLoopCycles {
		slog.Warn("role file: min_iterations above max_iterations clamped", "role", def.DisplayName, "min", def.MinLoopCycles, "max", def.MaxLoopCycles)
		def.MinLoopCycles = def.MaxLoopCycles
	}
	if def.MaxToolTurns > 0 && def.MinToolTurns > def.MaxToolTurns {
		slog.Warn("role file: min_turns above max_turns clamped", "role", def.DisplayName, "min", def.MinToolTurns, "max", def.MaxToolTurns)
		def.MinToolTurns = def.MaxToolTurns
	}

	return def
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
		MaxLoopCycles: d.MaxLoopCycles,
		MinLoopCycles: d.MinLoopCycles,
		MaxToolTurns:  d.MaxToolTurns,
		MinToolTurns:  d.MinToolTurns,
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
// frontmatter block (delimited by "---" lines) and converts the
// frontmatter into a RoleDef via the shared rolefile parser. The
// remaining markdown is stored in the Body field.
func parseRoleFile(data []byte) (RoleDef, error) {
	fm, body, err := rolefile.Parse(string(data))
	if err != nil {
		return RoleDef{}, err
	}
	return roleDefFrom(fm, body), nil
}
