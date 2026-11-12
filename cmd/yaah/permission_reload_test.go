package yaah

// permission_reload_test.go pins the run-boundary permission reload
// (the RoleWatcher contract applied to agents.default.permissions): the
// cache serves the seed until the file changes, swaps on change, keeps
// the last-known rules when the new file cannot be loaded, and a
// hand-built session without a cache keeps the frozen config value.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/permissions"
)

// writeReloadConfigs is intentionally absent: each test writes its own
// config lines so the YAML an assertion failed against is the YAML in
// the test body, one grep away.

func TestPermissionRulesCacheReloadsOnChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)

	seed := &config.Config{}
	seed.Agent.Default.Permissions = permissions.Rules{
		{Tool: "bash", Resource: "git push*", Effect: permissions.Ask},
	}
	c := newPermissionRulesCache(seed)

	// No config file: an absent file is the state a fresh session would
	// see — the empty ruleset, observed once without warnings, seed or
	// no seed (in reality the seed implies the file existed at
	// construction; its absence at a run boundary is a deletion).
	if got := c.current(); len(got) != 0 {
		t.Errorf("current() with no config file = %v, want empty", got)
	}

	// Write a config with a tightening rule: the next run sees it.
	body := "agents:\n  default:\n    permissions:\n      bash:\n        \"git push\": deny\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	got := c.current()
	want := permissions.Rules{{Tool: "bash", Resource: "git push", Effect: permissions.Deny}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("current() after change = %v, want %v", got, want)
	}

	// Unchanged file: same table stakes, no reload path taken.
	if got := c.current(); !reflect.DeepEqual(got, want) {
		t.Errorf("current() unchanged = %v, want %v", got, want)
	}

	// Edit again (widen): the next run sees the new rules.
	body = "agents:\n  default:\n    permissions:\n      bash: allow\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	got = c.current()
	want = permissions.Rules{{Tool: "bash", Effect: permissions.Allow}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("current() after second change = %v, want %v", got, want)
	}
}

func TestPermissionRulesCacheKeepsLastKnownOnBrokenConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)

	body := "agents:\n  default:\n    permissions:\n      bash: allow\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	c := newPermissionRulesCache(nil)
	want := permissions.Rules{{Tool: "bash", Effect: permissions.Allow}}
	if got := c.current(); !reflect.DeepEqual(got, want) {
		t.Fatalf("current() initial = %v, want %v", got, want)
	}

	// An unparseable config must not swap the live ruleset — nor crash,
	// nor narrow to the empty default.
	broken := "agents:\n  default:\n    permissions:\n      bash: [unparsable\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(broken), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	if got := c.current(); !reflect.DeepEqual(got, want) {
		t.Errorf("current() with broken config = %v, want last-known %v", got, want)
	}

	// A repairing edit does swap back in.
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	if got := c.current(); !reflect.DeepEqual(got, want) {
		t.Errorf("current() after repair = %v, want %v", got, want)
	}
}

// TestGlobalPermissionRulesFallsBackWithoutCache pins the hand-built
// session path: no cache, frozen config — the pre-reload behavior,
// byte-for-byte.
func TestGlobalPermissionRulesFallsBackWithoutCache(t *testing.T) {
	s := &agentSession{cfg: &config.Config{}}
	s.cfg.Agent.Default.Permissions = permissions.Rules{{Tool: "read", Effect: permissions.Allow}}
	if got := s.globalPermissionRules(); !reflect.DeepEqual(got, s.cfg.Agent.Default.Permissions) {
		t.Errorf("globalPermissionRules() without cache = %v, want %v", got, s.cfg.Agent.Default.Permissions)
	}
}
