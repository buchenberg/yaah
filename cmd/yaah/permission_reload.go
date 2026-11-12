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
// re-read, failed to parse, or parsed into a config Validate rejects)
// keeps the last-known rules and warns on stderr: a broken file
// tightening a session to nothing or widening it silently are both
// wrong, and the old rules are the last thing the user expressed that
// the harness could actually load.
//
// A DELETED config file is treated the same way once it has been seen.
// Absence at construction is a state — the no-config-yet user, whose
// ruleset is empty anyway. Absence AFTER a config was loaded is a
// deletion, and deleting the file is not how a rule is revoked:
// silently adopting the empty ruleset there would drop every global
// deny and ask the session was running under.
type permissionRulesCache struct {
	mu    sync.Mutex
	last  string
	rules permissions.Rules
}

// newPermissionRulesCache seeds the cache from the construction-time
// config so the first run boundary does not re-read what startup just
// read, and records the content hash it was built from so the first
// current() call has something to compare against. A construction with
// no readable config seeds the absent marker, so the file's later
// appearance counts as a change.
func newPermissionRulesCache(cfg *config.Config) *permissionRulesCache {
	c := &permissionRulesCache{}
	if cfg == nil {
		return c
	}
	c.rules = cfg.Agent.Default.Permissions
	path, err := config.ConfigPath()
	if err != nil {
		return c
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		c.last = contentHash(data)
	case os.IsNotExist(err):
		c.last = absentConfigHash
	}
	return c
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
		if os.IsNotExist(err) {
			switch c.last {
			case absentConfigHash:
				// Known absent: the no-config-yet state, already reconciled
				// once. Nothing changed, so nothing is said.
				return c.rules
			case "":
				// First observation with no seeded state: adopt the empty
				// ruleset a fresh session would see, silently.
				c.observe(absentConfigHash, nil)
				return c.rules
			default:
				// A config we loaded has vanished. Keep the rules and say so
				// — deleting the file is not how a rule is revoked, and an
				// accidental disappearance must not silently drop every deny
				// and ask the session was running under. The hash is left
				// alone, so a file that comes back unchanged settles back
				// onto these same rules instead of being re-read.
				fmt.Fprintf(os.Stderr, "warning: permission reload kept the last-known rules (%s disappeared; restore it, or revoke rules deliberately)\n", path)
				return c.rules
			}
		}
		fmt.Fprintf(os.Stderr, "warning: permission reload kept the last-known rules (reading %s: %v)\n", path, err)
		return c.rules
	}
	sum := contentHash(data)
	if sum == c.last {
		return c.rules
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: permission reload kept the last-known rules (new config rejected: %v)\n", err)
		return c.rules
	}
	// config.Load re-reads the file, so a write landing between our hash and
	// its parse would record a hash that does not describe the rules we took.
	// Skip this boundary and pick the edit up on the next one.
	if again, rerr := os.ReadFile(path); rerr != nil || contentHash(again) != sum {
		return c.rules
	}
	// Construction runs Validate as well as Load. Without it a config that
	// startup would refuse outright could still push its permissions stanza
	// live, so the parity the file header claims would not hold.
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: permission reload kept the last-known rules (new config rejected: %v)\n", err)
		return c.rules
	}
	c.observe(sum, cfg.Agent.Default.Permissions)
	return c.rules
}

// contentHash is the change detector: content, not mtime, so an edit cannot
// hide behind a coarse timestamp.
func contentHash(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
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
