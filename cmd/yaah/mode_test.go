package yaah

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent"
)

// The tool gate is derived from planModeSpec, so plan mode must return the
// spec's level and every other mode the full toolset.
func TestToolsLevelForMode(t *testing.T) {
	s := &agentSession{}
	cases := []struct {
		name string
		mode string
		want agent.ToolsLevel
	}{
		{"plan", acpModePlan, agent.ReadOnlyTools},
		{"build", acpModeBuild, agent.FullTools},
		{"empty defaults to build", "", agent.FullTools},
		{"unknown mode", "yolo", agent.FullTools},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.toolsLevelForMode(tc.mode); got != tc.want {
				t.Fatalf("toolsLevelForMode(%q) = %v; want %v", tc.mode, got, tc.want)
			}
		})
	}
}

// The spec is the single source of truth: whatever it declares must be what
// the gate enforces, and its regime sentence must match its level.
func TestPlanModeSpec_IsTheSourceOfTruth(t *testing.T) {
	if planModeSpec.Mode != acpModePlan {
		t.Fatalf("planModeSpec.Mode = %q; want %q", planModeSpec.Mode, acpModePlan)
	}
	if planModeSpec.ToolsLevel != agent.ReadOnlyTools {
		t.Fatalf("planModeSpec.ToolsLevel = %v; want agent.ReadOnlyTools", planModeSpec.ToolsLevel)
	}
	s := &agentSession{}
	if got := s.toolsLevelForMode(planModeSpec.Mode); got != planModeSpec.ToolsLevel {
		t.Fatalf("gate for %q = %v; want the spec's %v", planModeSpec.Mode, got, planModeSpec.ToolsLevel)
	}
	if got := regimeSentence(planModeSpec.ToolsLevel); !strings.Contains(got, "withheld") {
		t.Fatalf("regimeSentence(planModeSpec.ToolsLevel) = %q; want the withheld regime", got)
	}
	if got := regimeSentence(agent.FullTools); !strings.Contains(got, "do not mutate") {
		t.Fatalf("regimeSentence(agent.FullTools) = %q; want the policy-only regime", got)
	}
}

func TestRegimeSentence(t *testing.T) {
	cases := []struct {
		name  string
		level agent.ToolsLevel
		want  []string
		deny  []string
	}{
		{
			name:  "read-only says the tools are withheld",
			level: agent.ReadOnlyTools,
			want:  []string{"withheld", "inspection"},
			deny:  []string{"do not mutate"},
		},
		{
			name:  "full tools say the surface is available but policy-bounded",
			level: agent.FullTools,
			want:  []string{"available", "do not mutate", "approval"},
			deny:  []string{"withheld"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := regimeSentence(tc.level)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("regimeSentence(%v) = %q; want %q", tc.level, got, want)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(got, deny) {
					t.Errorf("regimeSentence(%v) = %q; must not contain %q", tc.level, got, deny)
				}
			}
		})
	}
}

// Both modes carry a directive — build mode's cue must not be the mere
// absence of the plan directive.
func TestModeDirective_BothModes(t *testing.T) {
	s := &agentSession{mainPrompt: "BASE PROMPT BODY"}

	cases := []struct {
		name string
		mode string
		want []string
		deny []string
	}{
		{
			name: "plan",
			mode: acpModePlan,
			want: []string{"BASE PROMPT BODY", "Plan mode is active", "withheld", "mode-change notice"},
			deny: []string{"do not mutate"},
		},
		{
			name: "build",
			mode: acpModeBuild,
			want: []string{"BASE PROMPT BODY", "Build mode is active", "available", "do not mutate", "mode-change notice"},
			deny: []string{"withheld"},
		},
		{
			name: "empty is build",
			mode: "",
			want: []string{"Build mode is active", "do not mutate"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.modeDirective(tc.mode)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("modeDirective(%q) missing %q:\n%s", tc.mode, want, got)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(got, deny) {
					t.Errorf("modeDirective(%q) must not contain %q:\n%s", tc.mode, deny, got)
				}
			}
			if got == s.mainPrompt {
				t.Fatalf("modeDirective(%q) returned mainPrompt unchanged; want an injected directive", tc.mode)
			}
		})
	}
}

// Every mode directive names who switches modes, through which surfaces, and
// that the switch is announced in-band — the facts the model could not state
// before.
func TestModeDirective_NamesLeavingMechanismAndNotice(t *testing.T) {
	s := &agentSession{mainPrompt: "BASE PROMPT BODY"}
	shared := []string{"/plan", "/build", "web palette", "session/set_mode", "question", "in-band", "mode-change notice"}
	for _, mode := range []string{acpModePlan, acpModeBuild} {
		t.Run(mode, func(t *testing.T) {
			got := s.modeDirective(mode)
			for _, want := range shared {
				if !strings.Contains(got, want) {
					t.Errorf("modeDirective(%q) missing %q:\n%s", mode, want, got)
				}
			}
		})
	}
}
