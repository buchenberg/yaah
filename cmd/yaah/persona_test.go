package yaah

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

// captureSlogWarnings redirects the default logger for the duration of
// the test and returns a function yielding everything logged at warn or
// above (same helper as internal/agent/subagent's tests).
func captureSlogWarnings(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string { return buf.String() }
}

// personaTestRegistry installs a registry with one primary-capable
// persona ("planner", mode all) and one delegate-only role ("grunt"),
// restoring the previous registry afterwards.
func personaTestRegistry(t *testing.T) {
	t.Helper()
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(map[string][]byte{
		"planner.md": []byte("---\nname: Planner\nmode: all\ntools: [read]\ndescription: plans changes\n---\nplan body"),
		"grunt.md":   []byte("---\nname: Grunt\nmode: subagent\ntools: [bash]\ndescription: does work\n---\nwork body"),
	}); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	saved := subagent.DefaultRegistry()
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(saved) })
}

// TestResolveSessionPersona pins the D4 default-selection chain: unset
// resolves to the built-in build persona; a primary-capable name
// (built-in or registry) resolves to itself; a delegate-only or
// missing name warns and falls back to build — the session is never
// failed over this key.
func TestResolveSessionPersona(t *testing.T) {
	personaTestRegistry(t)

	tests := []struct {
		name           string
		configured     string
		wantPersona    string
		wantConfigured string
		wantFallback   bool
		wantLog        string // empty: no warning may fire
	}{
		{"unset resolves to build", "", "build", "", false, ""},
		{"built-in plan resolves", "plan", "plan", "plan", false, ""},
		{"registry persona resolves", "planner", "planner", "planner", false, ""},
		{"delegate-only role falls back", "grunt", "build", "grunt", true, "falling back"},
		{"missing role falls back", "ghost", "build", "ghost", true, "falling back"},
		{"padded name is trimmed", "  plan  ", "plan", "plan", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureSlogWarnings(t)
			got := resolveSessionPersona(tt.configured)

			if got.Persona.Name != tt.wantPersona {
				t.Errorf("persona = %q, want %q", got.Persona.Name, tt.wantPersona)
			}
			if got.Configured != tt.wantConfigured {
				t.Errorf("Configured = %q, want %q", got.Configured, tt.wantConfigured)
			}
			if got.Fallback != tt.wantFallback {
				t.Errorf("Fallback = %v, want %v", got.Fallback, tt.wantFallback)
			}
			logged := logs()
			if tt.wantLog != "" {
				if !strings.Contains(logged, tt.wantLog) {
					t.Errorf("expected warning containing %q, log:\n%s", tt.wantLog, logged)
				}
				if !strings.Contains(logged, "configured="+tt.wantConfigured) {
					t.Errorf("warning lost the configured attribute, log:\n%s", logged)
				}
			} else if logged != "" {
				t.Errorf("unexpected warnings:\n%s", logged)
			}
		})
	}

	t.Run("miss reasons distinguish typo from delegate-only", func(t *testing.T) {
		logs := captureSlogWarnings(t)
		resolveSessionPersona("grunt")
		resolveSessionPersona("ghost")
		logged := logs()
		if !strings.Contains(logged, "reason=delegate-only") || !strings.Contains(logged, "configured=grunt") {
			t.Errorf("delegate-only miss not labeled, log:\n%s", logged)
		}
		if !strings.Contains(logged, "reason=missing") || !strings.Contains(logged, "configured=ghost") {
			t.Errorf("missing miss not labeled, log:\n%s", logged)
		}
	})
}

// TestResolveSessionPersonaWithoutRegistry covers the no-registry
// path: the built-ins still resolve, and a registry-only name falls
// back to build.
func TestResolveSessionPersonaWithoutRegistry(t *testing.T) {
	saved := subagent.DefaultRegistry()
	subagent.SetDefaultRoleRegistry(nil)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(saved) })

	logs := captureSlogWarnings(t)
	if got := resolveSessionPersona("plan"); got.Persona.Name != "plan" || got.Fallback {
		t.Errorf("plan without a registry = %+v, want the built-in persona", got)
	}
	if got := resolveSessionPersona("planner"); got.Persona.Name != "build" || !got.Fallback {
		t.Errorf("registry name without a registry = %+v, want fallback to build", got)
	}
	if logged := logs(); !strings.Contains(logged, "falling back") {
		t.Errorf("expected a fallback warning, log:\n%s", logged)
	}
}
