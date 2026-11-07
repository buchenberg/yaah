package subagent

import (
	"strings"
	"testing"
)

// TestPersonaBudgetFields pins yaah-21uk.8's persona surface: a
// registry persona carries its role file's budget fields for the
// ORCHESTRATOR loop, and the synthetic built-ins leave all three zero
// — no constraint, so the default session keeps today's limits
// exactly.
func TestPersonaBudgetFields(t *testing.T) {
	reg := NewRoleRegistry()
	if err := reg.LoadBytes(map[string][]byte{
		"boss.md": []byte("---\nname: Boss\nmode: primary\ntools: [read]\nmax_iterations: 30\nmax_turns: 20\ntimeout: 600\ndescription: runs the session\n---\nbody"),
	}); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	SetDefaultRoleRegistry(reg)
	defer SetDefaultRoleRegistry(nil)

	t.Run("registry persona carries the file budgets", func(t *testing.T) {
		p, ok := ResolvePersona("boss")
		if !ok {
			t.Fatal("boss did not resolve as a persona")
		}
		if p.MaxLoopCycles != 30 || p.MaxToolTurns != 20 || p.Timeout != 600 {
			t.Errorf("boss budgets = %d/%d/%d, want 30/20/600", p.MaxLoopCycles, p.MaxToolTurns, p.Timeout)
		}
	})

	t.Run("built-ins carry no constraint", func(t *testing.T) {
		for _, name := range []string{BuildPersonaName, PlanPersonaName} {
			p, ok := ResolvePersona(name)
			if !ok {
				t.Fatalf("%s did not resolve", name)
			}
			if p.MaxLoopCycles != 0 || p.MaxToolTurns != 0 || p.Timeout != 0 {
				t.Errorf("built-in %s budgets = %d/%d/%d, want all zero", name, p.MaxLoopCycles, p.MaxToolTurns, p.Timeout)
			}
		}
	})
}

// TestPersonaProviderModelWarnedAndIgnored pins yaah-21uk.8's routing
// stance: a persona file (mode primary/all) carrying provider/model
// loads fine but warns that routing is config-only; a persona without
// the keys warns nothing; a delegate-only file with the keys is out of
// the persona feature's scope and keeps its historical silence. The
// modeled fields the role actually runs with are never disturbed.
func TestPersonaProviderModelWarnedAndIgnored(t *testing.T) {
	tests := []struct {
		name     string
		front    string // frontmatter lines after the name
		wantMode string
		wantLog  string // empty: no warning may fire
	}{
		{"persona with provider", "mode: primary\nprovider: zai\n", "primary", "config-only"},
		{"persona with model", "mode: all\nmodel: glm-5\n", "all", "config-only"},
		{"persona with both", "mode: primary\nprovider: zai\nmodel: glm-5\n", "primary", "config-only"},
		{"persona without routing keys", "mode: primary\n", "primary", ""},
		{"delegate-only file with provider", "mode: subagent\nprovider: zai\n", "subagent", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureSlogWarnings(t)
			def, err := parseRoleFile([]byte("---\nname: Checker\n" + tt.front + "tools: [read]\n---\nbody"))
			if err != nil {
				t.Fatalf("parseRoleFile: %v", err)
			}
			if def.Mode != tt.wantMode {
				t.Errorf("RoleDef.Mode = %q, want %q", def.Mode, tt.wantMode)
			}
			if def.MaxLoopCycles != 0 || def.Timeout != 0 {
				t.Errorf("routing keys disturbed the modeled budgets: %d/%d, want 0/0", def.MaxLoopCycles, def.Timeout)
			}
			got := logs()
			if tt.wantLog != "" {
				if !strings.Contains(got, tt.wantLog) {
					t.Errorf("expected warning containing %q, log:\n%s", tt.wantLog, got)
				}
				if !strings.Contains(got, "role=Checker") {
					t.Errorf("warning lost the role attribute, log:\n%s", got)
				}
				if !strings.Contains(got, "provider=zai") && !strings.Contains(got, "model=glm-5") {
					t.Errorf("warning lost the routing attributes, log:\n%s", got)
				}
			} else if got != "" {
				t.Errorf("unexpected warnings:\n%s", got)
			}
		})
	}
}
