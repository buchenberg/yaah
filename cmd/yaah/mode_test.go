package yaah

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
)

// The tool gate derives from the persona alone: a read-only
// persona gets the inspection set, everything else the full toolset. There is
// no mode input left to vary.
func TestSampledToolsLevel_PersonaMatrix(t *testing.T) {
	plan, _ := subagent.ResolvePersona(subagent.PlanPersonaName)
	build, _ := subagent.ResolvePersona(subagent.BuildPersonaName)
	readonlyRegistry := subagent.Persona{Name: "auditor", Mode: "primary", ReadOnly: true}
	fullRegistry := subagent.Persona{Name: "builder", Mode: "primary"}

	cases := []struct {
		name    string
		persona subagent.Persona
		want    agent.ToolsLevel
	}{
		{"plan persona is read-only", plan, agent.ReadOnlyTools},
		{"build persona is full", build, agent.FullTools},
		{"read-only registry persona is read-only", readonlyRegistry, agent.ReadOnlyTools},
		{"full registry persona is full", fullRegistry, agent.FullTools},
		{"zero persona is full", subagent.Persona{}, agent.FullTools},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sampledToolsLevel(tc.persona); got != tc.want {
				t.Fatalf("sampledToolsLevel(%q) = %v; want %v", tc.persona.Name, got, tc.want)
			}
			s := &agentSession{persona: tc.persona}
			if got := s.effectiveToolsLevel(); got != tc.want {
				t.Fatalf("effectiveToolsLevel() = %v; want %v", got, tc.want)
			}
		})
	}
}

// The regime sentences are the generated wording authority: the read-only one
// names the withheld surface, the full one the policy-bounded surface.
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

// Both personas carry a directive — build's cue must not be the mere absence
// of the plan directive. Golden semantics: a default build session still gets
// the full-regime directive, a plan session the read-only one.
func TestModeDirective_BothPersonas(t *testing.T) {
	plan, _ := subagent.ResolvePersona(subagent.PlanPersonaName)
	build, _ := subagent.ResolvePersona(subagent.BuildPersonaName)

	cases := []struct {
		name    string
		persona subagent.Persona
		want    []string
		deny    []string
	}{
		{
			name:    "plan",
			persona: plan,
			want:    []string{"BASE PROMPT BODY", "Plan persona is active", "withheld", "agent-change notice"},
			deny:    []string{"do not mutate"},
		},
		{
			name:    "build",
			persona: build,
			want:    []string{"BASE PROMPT BODY", "Build persona is active", "available", "do not mutate", "agent-change notice"},
			deny:    []string{"withheld"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &agentSession{mainPrompt: "BASE PROMPT BODY", persona: tc.persona}
			got := s.modeDirective(tc.persona)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("modeDirective(%q) missing %q:\n%s", tc.persona.Name, want, got)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(got, deny) {
					t.Errorf("modeDirective(%q) must not contain %q:\n%s", tc.persona.Name, deny, got)
				}
			}
			if got == s.mainPrompt {
				t.Fatalf("modeDirective(%q) returned mainPrompt unchanged; want an injected directive", tc.persona.Name)
			}
		})
	}
}

// Every persona directive names who switches agents, through which surfaces,
// and that the switch is announced in-band — and never tells the model it can
// switch itself.
func TestModeDirective_NamesLeavingMechanismAndNotice(t *testing.T) {
	plan, _ := subagent.ResolvePersona(subagent.PlanPersonaName)
	build, _ := subagent.ResolvePersona(subagent.BuildPersonaName)
	shared := []string{"/agent", "web palette", "session/set_agent", "question", "in-band", "agent-change notice"}
	for _, persona := range []subagent.Persona{plan, build} {
		t.Run(persona.Name, func(t *testing.T) {
			s := &agentSession{mainPrompt: "BASE PROMPT BODY", persona: persona}
			got := s.modeDirective(persona)
			for _, want := range shared {
				if !strings.Contains(got, want) {
					t.Errorf("modeDirective(%q) missing %q:\n%s", persona.Name, want, got)
				}
			}
		})
	}
}

// Golden byte-stability: with no persona change, the per-run
// directive is byte-identical across repeated builds, and a switch round
// trip restores the original prompt bytes exactly — the directive is a pure
// function of the sampled persona.
func TestModeDirective_ByteStableAcrossRuns(t *testing.T) {
	plan, _ := subagent.ResolvePersona(subagent.PlanPersonaName)
	build, _ := subagent.ResolvePersona(subagent.BuildPersonaName)

	for _, persona := range []subagent.Persona{build, plan} {
		t.Run(persona.Name, func(t *testing.T) {
			s := &agentSession{mainPrompt: "BASE PROMPT BODY", persona: persona}
			first := s.modeDirective(persona)
			for i := 2; i <= 3; i++ {
				if again := s.modeDirective(persona); again != first {
					t.Fatalf("build %d directive differs from build 1:\n--- build 1 ---\n%s\n--- build %d ---\n%s", i, first, i, again)
				}
			}
		})
	}
}

// The identity anchor lets the persona directive sit immediately after a
// non-default identity block instead of falling to the end-of-prompt append.
// The block goes right after the custom identity and the rest of the prompt
// follows verbatim.
func TestModeDirective_CustomIdentityAnchor(t *testing.T) {
	custom := "BESPOKE ORCHESTRATOR PERSONA"
	plan, _ := subagent.ResolvePersona(subagent.PlanPersonaName)
	s := &agentSession{
		mainPrompt:  custom + "\n\n## Environment\nOS: test",
		identityEnd: len(custom),
		persona:     plan,
	}

	got := s.modeDirective(plan)

	if !strings.HasPrefix(got, custom+"\n\n## Session directives") {
		t.Fatalf("persona directive must sit immediately after the custom identity, got:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n\n## Environment\nOS: test") {
		t.Errorf("the base prompt must follow verbatim after the directive, got:\n%s", got)
	}
	idxDirectives := strings.Index(got, "## Session directives")
	idxEnv := strings.Index(got, "## Environment")
	if !(0 < idxDirectives && idxDirectives < idxEnv) {
		t.Errorf("wrong section order identity=0 directives=%d env=%d", idxDirectives, idxEnv)
	}
}

// Mode() is a pure derivation from the persona: the plan persona reads as
// the plan mode word, everything else — including a hand-built zero persona
// — as build. The session never stores a mode.
func TestModeDerivesFromPersona(t *testing.T) {
	if got := (&agentSession{}).Mode(); got != acpModeBuild {
		t.Fatalf("zero persona Mode() = %q; want %q", got, acpModeBuild)
	}
	if got := (&agentSession{persona: subagent.Persona{Name: subagent.PlanPersonaName, ReadOnly: true}}).Mode(); got != acpModePlan {
		t.Fatalf("plan persona Mode() = %q; want %q", got, acpModePlan)
	}
}

// The fresh-session resolution is the config default, straight through:
// an unset key resolves build (there is no process-level override branch
// left to fall through), and a store is never consulted.
func TestResolveSessionPersonaFor_FreshUnsetUsesBuild(t *testing.T) {
	got := resolveSessionPersonaFor(nil, SessionOptions{}, &config.Config{})
	if got.Persona.Name != subagent.BuildPersonaName || got.Fallback {
		t.Fatalf("fresh resolution = %+v; want the built-in build persona", got)
	}
}
