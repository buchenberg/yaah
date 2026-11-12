package yaah

// permission_reload_test.go pins the run-boundary permission reload (the
// RoleWatcher contract applied to agents.default.permissions): the cache
// serves the seed until the file changes, swaps on change, keeps the
// last-known rules when the new file cannot be loaded, parsed or validated,
// keeps them too when the file disappears after having been read, and a
// hand-built session without a cache keeps the frozen config value.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/permissions"
)

const (
	reloadAllowBody = "agents:\n  default:\n    permissions:\n      bash: allow\n"
	reloadDenyBody  = "agents:\n  default:\n    permissions:\n      bash: deny\n"
	// Same as reloadAllowBody plus an unrelated stanza, for the
	// "edit something else" case.
	reloadAllowWithPosture = "agents:\n  default:\n    approval: ask\n    permissions:\n      bash: allow\n"
	reloadAllowPostureOff  = "agents:\n  default:\n    approval: allow\n    permissions:\n      bash: allow\n"
	reloadNoRulesBody      = "agents:\n  default:\n    approval: ask\n"
	// Parses, but Validate rejects it: startup refuses this config, so the
	// reload must not push its permissions stanza live either.
	reloadInvalidBody = "agents:\n  default:\n    approval: banana\n    permissions:\n      bash: allow\n"
)

// reloadConfig writes body to a fresh YAAH_HOME config.yaml and returns the
// path, so each test starts from a clean config resolution.
func reloadConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)
	path := filepath.Join(home, "config.yaml")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// seededCache builds the cache the way construction does: from the config
// the file currently describes.
func seededCache(t *testing.T) *permissionRulesCache {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return newPermissionRulesCache(cfg)
}

func reloadWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func wantRules(effect permissions.Effect) permissions.Rules {
	return permissions.Rules{{Tool: "bash", Effect: effect}}
}

// TestPermissionRulesCacheSeedsFromConstruction pins the seeding: startup
// already parsed this file, so the first run boundary must not re-read it.
// The seed is what a session sees until the file actually changes.
func TestPermissionRulesCacheSeedsFromConstruction(t *testing.T) {
	reloadConfig(t, reloadAllowBody)
	c := seededCache(t)

	if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Allow)) {
		t.Errorf("first current() = %v, want the construction-time rules", got)
	}
	if c.last == "" || c.last == absentConfigHash {
		t.Errorf("cache did not record the file's content hash at construction (last = %q)", c.last)
	}
}

// TestPermissionRulesCacheReloadsOnChange pins the reload: a tightening and a
// widening both land at the next boundary, and an unchanged file is served
// from the cache.
func TestPermissionRulesCacheReloadsOnChange(t *testing.T) {
	path := reloadConfig(t, reloadAllowBody)
	c := seededCache(t)

	reloadWrite(t, path, reloadDenyBody)
	if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Deny)) {
		t.Errorf("after tightening, current() = %v, want deny", got)
	}

	// Unchanged file: same rules, and the hash is stable.
	before := c.last
	if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Deny)) {
		t.Errorf("unchanged file, current() = %v, want deny", got)
	}
	if c.last != before {
		t.Errorf("unchanged file moved the hash: %q -> %q", before, c.last)
	}

	reloadWrite(t, path, reloadAllowBody)
	if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Allow)) {
		t.Errorf("after widening, current() = %v, want allow", got)
	}
}

// TestPermissionRulesCacheKeepsLastKnownOnBrokenConfig pins the broken-file
// fallback, including the repair. The repair writes a DIFFERENT valid ruleset
// so the assertion cannot pass on an unchanged hash: a regression that never
// re-runs the load path would leave the deny unseen.
func TestPermissionRulesCacheKeepsLastKnownOnBrokenConfig(t *testing.T) {
	path := reloadConfig(t, reloadAllowBody)
	c := seededCache(t)

	reloadWrite(t, path, "agents: [this is not a mapping\n")
	if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Allow)) {
		t.Errorf("broken config, current() = %v, want the last-known allow", got)
	}

	reloadWrite(t, path, reloadDenyBody)
	if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Deny)) {
		t.Errorf("repaired config, current() = %v, want deny (the repair must be picked up)", got)
	}
}

// TestPermissionRulesCacheKeepsLastKnownWhenConfigDisappears pins the
// deletion rule: once a config has been read, its disappearance keeps the
// rules rather than silently emptying the session's deny and ask rules.
func TestPermissionRulesCacheKeepsLastKnownWhenConfigDisappears(t *testing.T) {
	path := reloadConfig(t, reloadDenyBody)
	c := seededCache(t)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Deny)) {
		t.Errorf("deleted config, current() = %v, want the last-known deny", got)
	}

	// A file that comes back unchanged settles back onto the same rules.
	reloadWrite(t, path, reloadDenyBody)
	if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Deny)) {
		t.Errorf("restored config, current() = %v, want deny", got)
	}
}

// TestPermissionRulesCacheRejectsInvalidConfig pins the construction parity:
// startup runs Load AND Validate, so a config Validate refuses must not push
// its permissions stanza live.
func TestPermissionRulesCacheRejectsInvalidConfig(t *testing.T) {
	path := reloadConfig(t, reloadAllowBody)
	c := seededCache(t)

	reloadWrite(t, path, reloadInvalidBody)
	if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Allow)) {
		t.Errorf("invalid config, current() = %v, want the last-known allow", got)
	}
}

// TestPermissionRulesCacheStanzaCases pins the three edits a user is most
// likely to make: removing the stanza (the natural "turn this off" action),
// editing a different stanza, and touching only a comment.
func TestPermissionRulesCacheStanzaCases(t *testing.T) {
	t.Run("stanza removed yields the empty ruleset", func(t *testing.T) {
		path := reloadConfig(t, reloadAllowWithPosture)
		c := seededCache(t)
		reloadWrite(t, path, reloadNoRulesBody)
		if got := c.current(); len(got) != 0 {
			t.Errorf("current() = %v, want the empty ruleset a fresh session would see", got)
		}
	})

	t.Run("unrelated stanza leaves the rules alone", func(t *testing.T) {
		path := reloadConfig(t, reloadAllowWithPosture)
		c := seededCache(t)
		before := c.last
		reloadWrite(t, path, reloadAllowPostureOff)
		if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Allow)) {
			t.Errorf("current() = %v, want the unchanged allow", got)
		}
		if c.last == before {
			t.Log("hash unchanged: the edit was not observed at all")
		}
	})

	t.Run("comment-only edit keeps the rules", func(t *testing.T) {
		path := reloadConfig(t, "# before\n"+reloadAllowBody)
		c := seededCache(t)
		reloadWrite(t, path, "# after\n"+reloadAllowBody)
		if got := c.current(); !reflect.DeepEqual(got, wantRules(permissions.Allow)) {
			t.Errorf("current() = %v, want the unchanged allow", got)
		}
	})
}

// TestGlobalPermissionRulesWithoutCache pins the hand-built session path: no
// cache means the construction-time value, exactly as before the reload
// existed.
func TestGlobalPermissionRulesWithoutCache(t *testing.T) {
	s := &agentSession{cfg: &config.Config{}}
	s.cfg.Agent.Default.Permissions = permissions.Rules{{Tool: "read", Effect: permissions.Allow}}
	if got := s.globalPermissionRules(); !reflect.DeepEqual(got, s.cfg.Agent.Default.Permissions) {
		t.Errorf("globalPermissionRules() without cache = %v, want %v", got, s.cfg.Agent.Default.Permissions)
	}
}
