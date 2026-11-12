package yaah

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
)

// The tool gate derives from the session's mode axis alone: a read-only
// session gets the inspection set, everything else the full toolset. The
// persona has no say — read-only-ness is session state, not a persona
// property (decouple-persona-mode-approval).
func TestSessionToolsLevel_ModeMatrix(t *testing.T) {
	plan, _ := subagent.ResolvePersona(subagent.PlanPersonaName)
	build, _ := subagent.ResolvePersona(subagent.BuildPersonaName)

	cases := []struct {
		name     string
		persona  subagent.Persona
		readOnly bool
		want     agent.ToolsLevel
	}{
		{"read-only session is read-only regardless of persona", plan, true, agent.ReadOnlyTools},
		{"full session is full regardless of persona", plan, false, agent.FullTools},
		{"build persona in a read-only session is read-only", build, true, agent.ReadOnlyTools},
		{"build persona in a full session is full", build, false, agent.FullTools},
		{"zero persona in a full session is full", subagent.Persona{}, false, agent.FullTools},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionToolsLevel(tc.readOnly); got != tc.want {
				t.Fatalf("sessionToolsLevel(%v) = %v; want %v", tc.readOnly, got, tc.want)
			}
			s := &agentSession{persona: tc.persona, readOnly: tc.readOnly}
			if got := sessionToolsLevel(s.readOnly); got != tc.want {
				t.Fatalf("effectiveToolsLevel() = %v; want %v", got, tc.want)
			}
		})
	}
}

// The regime sentences are the generated wording authority: the read-only one
// names the withheld surface in the mode axis's vocabulary, the full one the
// policy-bounded surface. Neither mentions a persona name.
func TestRegimeSentence(t *testing.T) {
	cases := []struct {
		name  string
		level agent.ToolsLevel
		want  []string
		deny  []string
	}{
		{
			name:  "read-only says the mode and the withheld surface",
			level: agent.ReadOnlyTools,
			want:  []string{"read-only mode", "inspection"},
			deny:  []string{"full access", "plan persona"},
		},
		{
			name:  "full tools say the surface is available but policy-bounded",
			level: agent.FullTools,
			want:  []string{"full access", "available", "do not mutate", "approval"},
			deny:  []string{"withheld", "read-only"},
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

// Both axes carry a directive — the regime sentence comes from the mode
// axis, the opening word from the persona. Golden semantics: a full session
// running plan still gets the full-access sentence; a read-only session
// running build gets the read-only one.
func TestModeDirective_BothAxes(t *testing.T) {
	plan, _ := subagent.ResolvePersona(subagent.PlanPersonaName)
	build, _ := subagent.ResolvePersona(subagent.BuildPersonaName)

	cases := []struct {
		name     string
		persona  subagent.Persona
		readOnly bool
		want     []string
		deny     []string
	}{
		{
			name:     "plan persona, read-only",
			persona:  plan,
			readOnly: true,
			want:     []string{"BASE PROMPT BODY", "Plan persona is active", "read-only mode", "agent-change notice"},
			deny:     []string{"full access"},
		},
		{
			name:     "plan persona, full access — the persona does not force the mode",
			persona:  plan,
			readOnly: false,
			want:     []string{"BASE PROMPT BODY", "Plan persona is active", "full access"},
			deny:     []string{"read-only mode"},
		},
		{
			name:     "build persona, read-only — the mode is session state",
			persona:  build,
			readOnly: true,
			want:     []string{"BASE PROMPT BODY", "Build persona is active", "read-only mode"},
			deny:     []string{"full access"},
		},
		{
			name:     "build persona, full access",
			persona:  build,
			readOnly: false,
			want:     []string{"BASE PROMPT BODY", "Build persona is active", "full access", "do not mutate"},
			deny:     []string{"read-only mode"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &agentSession{mainPrompt: "BASE PROMPT BODY", persona: tc.persona, readOnly: tc.readOnly}
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

// Every directive names who switches agents, through which surfaces,
// and that the switch is announced in-band — and never tells the model it can
// switch itself. The supersession clause is gated on `switched`: a fresh
// session's first build has no earlier persona to supersede (asserting
// false history there would be its own defect), so this test pins its
// ABSENCE on hand-built sessions; the presence path is pinned wherever a
// real SetPersona switch happens (double-switch guard).
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
			if strings.Contains(got, "supersedes") {
				t.Errorf("unswitched session's modeDirective asserts a superseded earlier persona:\n%s", got)
			}
		})
	}

	t.Run("switched session gains the supersession clause", func(t *testing.T) {
		s := &agentSession{mainPrompt: "BASE PROMPT BODY", persona: plan, switched: true}
		got := s.modeDirective(plan)
		for _, want := range []string{
			"supersedes any persona named earlier in this conversation",
			"ignore that persona's character, voice and style",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("switched modeDirective missing %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "style and complaints") {
			t.Errorf("supersession wording is report-shaped (complaints), want persona-neutral:\n%s", got)
		}
	})
}

// Golden byte-stability: with no axis change, the per-run
// directive is byte-identical across repeated builds — the directive is a
// pure function of the sampled persona and mode.
func TestModeDirective_ByteStableAcrossRuns(t *testing.T) {
	plan, _ := subagent.ResolvePersona(subagent.PlanPersonaName)
	build, _ := subagent.ResolvePersona(subagent.BuildPersonaName)

	for _, persona := range []subagent.Persona{build, plan} {
		for _, readOnly := range []bool{false, true} {
			t.Run(persona.Name, func(t *testing.T) {
				s := &agentSession{mainPrompt: "BASE PROMPT BODY", persona: persona, readOnly: readOnly}
				first := s.modeDirective(persona)
				for i := 2; i <= 3; i++ {
					if again := s.modeDirective(persona); again != first {
						t.Fatalf("build %d directive differs from build 1:\n--- build 1 ---\n%s\n--- build %d ---\n%s", i, first, i, again)
					}
				}
			})
		}
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
		readOnly:    true,
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

// Mode() is a pure derivation from the session's mode axis: a read-only
// session reads as the plan mode word, everything else as build — regardless
// of which persona runs. The persona does not force the mode word.
func TestModeDerivesFromReadOnly(t *testing.T) {
	if got := (&agentSession{}).Mode(); got != acpModeBuild {
		t.Fatalf("zero session Mode() = %q; want %q", got, acpModeBuild)
	}
	if got := (&agentSession{readOnly: true}).Mode(); got != acpModePlan {
		t.Fatalf("read-only session Mode() = %q; want %q", got, acpModePlan)
	}
	planPersona := subagent.Persona{Name: subagent.PlanPersonaName}
	if got := (&agentSession{persona: planPersona}).Mode(); got != acpModeBuild {
		t.Fatalf("plan persona in a full session Mode() = %q; want %q (axes are independent)", got, acpModeBuild)
	}
}

// The axis accessors round-trip under the lock, and SetApprovalMode
// refuses an invalid value instead of storing it.
func TestAxisAccessors(t *testing.T) {
	s := &agentSession{}
	if s.IsReadOnly() {
		t.Fatal("zero session IsReadOnly() = true; want false")
	}
	if got := s.ApprovalMode(); got != "ask" {
		t.Fatalf("zero session ApprovalMode() = %q; want ask", got)
	}
	s.SetReadOnly(true)
	if !s.IsReadOnly() || s.Mode() != acpModePlan {
		t.Fatalf("SetReadOnly(true) did not take: readOnly=%v mode=%q", s.IsReadOnly(), s.Mode())
	}
	if err := s.SetApprovalMode("deny"); err != nil {
		t.Fatalf("SetApprovalMode(deny): %v", err)
	}
	if got := s.ApprovalMode(); got != "deny" {
		t.Fatalf("ApprovalMode() = %q; want deny", got)
	}
	if err := s.SetApprovalMode("sometimes"); err == nil {
		t.Fatal("SetApprovalMode(sometimes) accepted an invalid value")
	}
	if got := s.ApprovalMode(); got != "deny" {
		t.Fatalf("rejected SetApprovalMode changed the value: %q", got)
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

// The mode axis default comes from agents.default.mode: read-only only on
// the exact value, everything else (empty, typo) full.
func TestResolveReadOnlyMode(t *testing.T) {
	if resolveReadOnlyMode(&config.Config{}) {
		t.Fatal("empty config resolved read-only; want full")
	}
	cfg := &config.Config{}
	cfg.Agent.Default.Mode = "read-only"
	if !resolveReadOnlyMode(cfg) {
		t.Fatal("agents.default.mode read-only resolved full")
	}
	cfg.Agent.Default.Mode = "readonly"
	if resolveReadOnlyMode(cfg) {
		t.Fatal("typo'd mode value resolved read-only; want full (validation catches typos)")
	}
}
