package yaah

// permission_reload.go gives the global permission ruleset the same
// hot-reload guarantee roles already have (W6's RoleWatcher): an edit to
// config.yaml's permissions stanza takes effect at the next run boundary
// of a long-lived session, without a restart. Before this the rules were
// frozen at construction — a deny added mid-session (uncovering a
// dangerous command, tightening a path) never reached a session that had
// already started, which is the exact staleness gap the RoleWatcher's
// doc comment records for roles ("kept serving stale roles for the rest
// of a long session").
//
// Scope mirrors the RoleWatcher precedent deliberately ("Refresh is
// registry-only"): the cache re-reads exactly ONE stanza —
// agents.default.permissions. Model routing, provider credentials,
// budgets, mode, and approval postures stay frozen at construction —
// live-swapping those would change what a running session already
// promised, and mid-session switches already exist for mode and
// approval through their own surfaces.
//
// Application point is the run boundary the loop builder already is:
// one hash check per RunPrompt, evaluated between turns (never inside
// a tool call — the loop's authorization derives once per session from
// config by design, and a mid-call rule swap would break the
// "decision is a value" property Authorize documents). A run that
// starts with the old config finishes with it; the next one sees the
// new rules. That is also how the /mode switch is specified to behave
// ("effective at next run") — permissions now follow the same contract.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sync"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/permissions"
)

// permissionRulesCache holds the session's live view of the global
// permission ruleset, seeded from the config the session was built with
// and refreshed from ConfigPath() whenever the file's content hash
// changes. Content, not mtime — the same choice RoleWatcher made, for
// the same reason: timestamp granularity must never miss an edit.
//
// A reload that cannot complete (config vanished beneath us, failed to
// re-read, failed to parse) keeps the last-known rules and warns on
// stderr: a broken file tightening a session to nothing or widening it
// silently are both wrong, and the old rules are the last thing the
// user expressed that the harness could actually load.
type permissionRulesCache struct {
	mu    sync.Mutex
	last  string
	rules permissions.Rules
}

// newPermissionRulesCache seeds the cache from the construction-time
// config so the first run boundary does not re-read what startup just
// read. The seed's rules and the file's hash are then compared from
// the first current() call on.
func newPermissionRulesCache(cfg *config.Config) *permissionRulesCache {
	if cfg == nil {
		return &permissionRulesCache{}
	}
	return &permissionRulesCache{rules: cfg.Agent.Default.Permissions}
}

// current returns the global permission ruleset for this run, reloading
// from ConfigPath() only when the file's content hash changed since the
// last observed state.
func (c *permissionRulesCache) current() permissions.Rules {
	c.mu.Lock()
	defer c.mu.Unlock()

	path, err := config.ConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: permission reload disabled (config path: %v)\n", err)
		return c.rules
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// An absent config file is a state, not a failure — the same
		// distinction config.Load makes (NotExist yields the built-in
		// defaults without a word). Absent means the empty ruleset a
		// fresh session would see, observed once and silently, so the
		// common no-config-yet user gets no per-run warning spam.
		// Only a file that exists but cannot be read warns and keeps
		// the last-known rules.
		if os.IsNotExist(err) {
			c.observe(absentConfigHash, nil)
			return c.rules
		}
		fmt.Fprintf(os.Stderr, "warning: permission reload kept the last-known rules (reading %s: %v)\n", path, err)
		return c.rules
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	if sum == c.last {
		return c.rules
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: permission reload kept the last-known rules (new config rejected: %v)\n", err)
		return c.rules
	}
	c.observe(sum, cfg.Agent.Default.Permissions)
	return c.rules
}

// absentConfigHash is the content hash stand-in for a config file that
// does not exist. It cannot collide with a hex SHA-256.
const absentConfigHash = "(absent)"

// observe records the state one run boundary decided on, so identical
// states are skipped next time and a file that re-appears with its old
// content still counts as a change (absent → present is observed through
// a differing hash).
func (c *permissionRulesCache) observe(sum string, rules permissions.Rules) {
	c.last = sum
	c.rules = rules
}

// globalPermissionRules returns the global ruleset this run's loop
// should evaluate. A wired session consults the reload cache; a
// hand-built test session (no cache) keeps today's behavior exactly —
// the frozen construction-time value.
func (s *agentSession) globalPermissionRules() permissions.Rules {
	if s.permRules == nil {
		return s.cfg.Agent.Default.Permissions
	}
	return s.permRules.current()
}
