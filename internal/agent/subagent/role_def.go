package subagent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/permissions"
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
	Description string
	Contract    ContractDef
	Tools       []string
	// Output is the dispatch output contract ("report" or "proposal");
	// empty means proposal (derived routing keys off this, see lane.go).
	Output string
	// Mode is the agent capability (yaah-21uk D1): "primary" is
	// orchestrator-eligible, "subagent" delegate-only, "all" both.
	// Normalized on load — see roleDefFrom; delegate-surface filtering
	// keys off it (delegatable.go).
	Mode string
	// SystemReplace mirrors the role file's `system: replace` (parity
	// W2): the body replaces the embedded identity in the session's
	// system prompt instead of layering after it. Normalized on load —
	// see roleDefFrom.
	SystemReplace bool
	// Provider and Model are the role's routing (parity W1, yaah-21uk.15):
	// where the role's session (persona) or dispatch (delegate) runs.
	// Empty means inherit — personas fall back to agents.default,
	// delegates to the resolution chain's next entry.
	Provider string
	Model    string
	// Temperature and TopP are per-role sampling overrides passed
	// through to the provider request (parity W1.4). nil leaves the
	// provider's default; an explicit 0 is a real setting.
	Temperature   *float64
	TopP          *float64
	MaxLoopCycles int
	MinLoopCycles int // per-call overrides may not go below; 0 = none
	MaxToolTurns  int
	MinToolTurns  int // per-call overrides may not go below; 0 = none
	JSONMode      bool
	// Timeout is the role's default per-dispatch budget in seconds;
	// 0 = no timeout. Roles served by slow local models need a generous
	// budget — even a trivial completion can take 30s+ there, so a tight
	// per-role timeout kills runs that would have finished (#319).
	Timeout int

	// Permissions is the role's permission ruleset (parity W5): the
	// parsed `permissions:` frontmatter. Consumed on BOTH paths: the
	// session-persona path (registryPersona → the session loop's
	// ruleset, appended after the global config rules) and the dispatch
	// path (ToProfile → the sub-agent's own rules, with the parent's
	// inherited ceiling appended after so an inherited deny can never be
	// widened by the role file). Passing the rules through verbatim
	// (they are validated at parse time) — no normalization here, same
	// as Tools.
	Permissions []permissions.Rule

	// Headless is the role file's `headless:` declaration: what a headless
	// run does with this role's `ask` rules. "deny" (the only accepted
	// value) states that the dispatch path's refusal IS the intent, and
	// silences validateHeadlessAsk's load-time warning. Empty is the
	// historical default. Nothing keys dispatch behavior off it — a
	// sub-agent always refuses ask — so the value is intent, reported at
	// load rather than enforced.
	Headless string

	// Body is the role's markdown system-prompt body below the
	// frontmatter fence.
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
		Output:        normalizeRoleOutput(fm.Output, fm.Name),
		Mode:          normalizeRoleMode(fm.Mode, fm.Name),
		SystemReplace: normalizeRoleSystem(fm.System, fm.Name),
		Permissions:   []permissions.Rule(fm.Permissions),
		Headless:      fm.Headless,
		MaxLoopCycles: fm.MaxLoopCycles,
		MinLoopCycles: fm.MinLoopCycles,
		MaxToolTurns:  fm.MaxToolTurns,
		MinToolTurns:  fm.MinToolTurns,
		JSONMode:      fm.JSONMode,
		Timeout:       fm.Timeout,
		Provider:      fm.Provider,
		Model:         fm.Model,
		Temperature:   fm.Temperature,
		TopP:          fm.TopP,
		Body:          body,
	}

	// Clamp invalid budget floors instead of failing: a broken project
	// role must not brick startup (plan §4.6). Config-level floors are
	// hard-validated at load; role files only warn.
	// The same honesty covers `system: replace` on a delegate-only role:
	// nothing consumes RoleProfile.SystemReplace (dispatch prompts are the
	// sub-agent identity + layered role guidance), so the key would be
	// silently dead there — warn so the file's author knows.
	if def.SystemReplace && def.Mode == "subagent" {
		slog.Warn("role file: system: replace has no effect on delegate-only roles (it binds only session personas)", "role", def.DisplayName)
	}
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

	// Routing now comes FROM the file (parity W1, yaah-21uk.15): the
	// Provider/Model/Temperature/TopP keys map into the fields above for
	// every role. Dispatch resolution layers them as
	// call > config role entry (agents.subagent.roles, config-authoritative)
	// > role file > parent session model > config default.

	return def
}

// headlessPolicyDeny is the only value the role file's `headless:` key
// accepts. A sub-agent dispatch refuses an `ask` rule either way (a
// dispatch has no approval bridge, and letting the call through would
// quietly widen the rule to allow); declaring the policy is how a file
// says the refusal is what it meant, not a gap somebody forgot to close.
const headlessPolicyDeny = "deny"

// validateHeadlessAsk reports, at load, a subagent-capable role file that
// declares `ask` permission rules without declaring a headless policy.
//
// The rules are not inert any more — the dispatch gate
// (agent.Loop.admitTool) refuses an ask rule on a sub-agent loop — but the
// author still cannot see the mismatch from the file: they wrote "ask me
// first", and a dispatch has nobody to ask, so the boundary they intended
// arrives as a mid-run refusal that reads like a harness bug. Naming the
// role and its ask rules once per load is what closes that gap.
//
// This is the earliest point the distinction exists: roleDefFrom is where
// the mode axis is normalized, so "subagent-capable" (mode `subagent` or
// `all`, including a missing key) is finally known. Primary-only files are
// exempt — a persona runs inside a session, where the approval middleware
// is installed and ask works.
//
// Warn, not error: a broken project role must not brick startup (§4.6),
// the same rule the budget clamps follow. The warning fires once per
// registry load per role — startup, /reload-roles, and the reload every
// role-tool mutation triggers — never once per dispatch.
func validateHeadlessAsk(roleName string, def RoleDef) {
	if def.Mode == "primary" {
		return
	}
	asks := permissions.AskRules(def.Permissions)
	if len(asks) == 0 {
		return
	}
	switch def.Headless {
	case headlessPolicyDeny:
		return
	case "":
		slog.Warn("role file: `ask` permission rules cannot prompt on a sub-agent dispatch; a sub-agent role refuses them — declare `headless: deny` to state that the refusal is intended, or use `deny`",
			"role", roleName, "rules", describeAskRules(asks))
	default:
		slog.Warn("role file: unknown headless value; a sub-agent dispatch refuses `ask` rules regardless of it",
			"role", roleName, "value", def.Headless, "rules", describeAskRules(asks))
	}
}

// describeAskRules renders ask rules for one log line as `tool:resource`,
// or the bare tool when the rule matches every call of it.
func describeAskRules(rules []permissions.Rule) string {
	parts := make([]string, len(rules))
	for i, rule := range rules {
		if rule.Resource == "" {
			parts[i] = rule.Tool
			continue
		}
		parts[i] = rule.Tool + ":" + rule.Resource
	}
	return strings.Join(parts, ", ")
}

// normalizeRoleMode validates the role file's mode field (yaah-21uk D1;
// missing-key default flipped to "all" by the opencode-family parity W3).
// "primary", "subagent" and "all" pass through. A missing key resolves to
// "all" — family symmetry with opencode/Kilo agent files, which are
// orchestrator-capable unless marked otherwise; projects that want a
// file delegate-only say `mode: subagent` explicitly. An unknown value
// warns and fails CLOSED to "subagent": a typo'd delegate-only file must
// not become orchestrator-eligible — its body would become a session
// system prompt and its budget fields would bind the loop — so the
// missing-key and invalid-value defaults deliberately point in opposite
// directions (parity governs the absence, safety governs the mistake).
func normalizeRoleMode(v, roleName string) string {
	switch v {
	case "":
		return "all"
	case "primary", "subagent", "all":
		return v
	default:
		slog.Warn("role file: unknown mode value; falling back to subagent", "role", roleName, "value", v)
		return "subagent"
	}
}

// normalizeRoleSystem validates the role file's `system:` field (parity
// W2). Missing keeps the default layered behavior — the body renders as
// persona guidance after the embedded identity. "replace" swaps the body
// in FOR the embedded identity; environment, project instructions,
// memory, skills, and session directives still layer after. An unknown
// value warns and falls back to the layer: layering is where W1-era
// files already ran, and a typo'd replace must not silently promote the
// body to the session's identity — the one direction that cannot be
// undone by the belt or mode axes.
func normalizeRoleSystem(v, roleName string) bool {
	switch v {
	case "":
		return false
	case "replace":
		return true
	default:
		slog.Warn("role file: unknown system value; falling back to layer", "role", roleName, "value", v)
		return false
	}
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
		Output:        d.Output,
		Mode:          d.Mode,
		SystemReplace: d.SystemReplace,
		Provider:      d.Provider,
		Model:         d.Model,
		Temperature:   d.Temperature,
		TopP:          d.TopP,
		MaxLoopCycles: d.MaxLoopCycles,
		MinLoopCycles: d.MinLoopCycles,
		MaxToolTurns:  d.MaxToolTurns,
		MinToolTurns:  d.MinToolTurns,
		JSONMode:      d.JSONMode,
		Timeout:       time.Duration(d.Timeout) * time.Second,
		Permissions:   d.Permissions,
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
		validateHeadlessAsk(roleName, def)
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
		if len(data) > maxRoleFileBytes {
			slog.Warn("role file exceeds size cap, truncating",
				"path", path, "cap_bytes", maxRoleFileBytes, "actual_bytes", len(data))
			data = data[:maxRoleFileBytes]
		}
		def, err := parseRoleFile(data)
		if err != nil {
			return fmt.Errorf("role %q: %w", path, err)
		}
		validateHeadlessAsk(roleName, def)
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

// maxRoleFileBytes caps role/persona files read from disk, mirroring the
// 64 KiB AGENTS.md/CLAUDE.md cap (yaah-21uk.17). A file that exceeds it is
// truncated at the byte boundary; the frontmatter sits at the top, well
// under the cap, so truncation only ever cuts the body tail.
const maxRoleFileBytes = 64 * 1024

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
