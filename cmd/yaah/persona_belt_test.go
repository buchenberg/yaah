package yaah

import (
	"slices"
	"testing"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/tools"
)

// The belt/persona seam (yaah-21uk.7): a persona's tools list narrows
// agents.default.tools through the same tools.NewBelt the configured
// belt has always used — narrow-only, one enforcement point. The
// golden rule throughout: the built-in build persona names no tools,
// so today's surface, warnings and prompt stay byte-identical.

func TestEffectiveBeltNames(t *testing.T) {
	tests := []struct {
		name       string
		configured []string
		persona    []string
		want       []string
	}{
		// The four belt/persona combinations.
		{"neither set passes nothing through", nil, nil, nil},
		{"configured belt only is today's behavior", []string{"read", "grep"}, nil, []string{"read", "grep"}},
		{"persona only becomes the belt", nil, []string{"read", "grep"}, []string{"read", "grep"}},
		{"both set intersect", []string{"read", "grep", "bash"}, []string{"read", "edit"}, []string{"read"}},
		{"disjoint intersection is empty, not zero tools", []string{"read"}, []string{"bash"}, []string{}},
		// Order and normalization.
		{"intersection keeps the configured belt's order", []string{"bash", "read"}, []string{"read", "bash"}, []string{"bash", "read"}},
		{"duplicate persona names are harmless (the belt dedups)", nil, []string{"read", "read"}, []string{"read", "read"}},
		{"names are trimmed like the belt trims them", []string{" read ", "grep"}, []string{"read", "  edit  "}, []string{"read"}},
		{"blank persona entries drop out", nil, []string{"", "read", "   "}, []string{"read"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveBeltNames(tt.configured, tt.persona)
			if !slices.Equal(got, tt.want) {
				t.Errorf("effectiveBeltNames(%q, %q) = %q, want %q", tt.configured, tt.persona, got, tt.want)
			}
		})
	}

	t.Run("golden: built-in build persona leaves the belt untouched", func(t *testing.T) {
		buildPersona, ok := subagent.ResolvePersona(subagent.BuildPersonaName)
		if !ok {
			t.Fatal("build persona did not resolve")
		}
		if len(buildPersona.Tools) != 0 {
			t.Fatalf("build persona carries tools %v; the golden gate requires none", buildPersona.Tools)
		}
		got := effectiveBeltNames([]string{"read", "bash"}, buildPersona.Tools)
		if !slices.Equal(got, []string{"read", "bash"}) {
			t.Errorf("build persona rewrote the configured belt: %q", got)
		}
		if got := effectiveBeltNames(nil, buildPersona.Tools); got != nil {
			t.Errorf("build persona turned an unset belt into %q; want nil (full surface)", got)
		}
	})

	t.Run("golden: built-in plan persona does not narrow", func(t *testing.T) {
		planPersona, ok := subagent.ResolvePersona(subagent.PlanPersonaName)
		if !ok {
			t.Fatal("plan persona did not resolve")
		}
		if got := effectiveBeltNames(nil, planPersona.Tools); got != nil {
			t.Errorf("plan persona produced a belt %q; its restriction is the read-only level, not a belt", got)
		}
	})

	t.Run("golden: default session keeps the full surface and a silent belt", func(t *testing.T) {
		sp := resolveSessionPersona("agents.default.agent", "")
		if sp.Persona.Name != subagent.BuildPersonaName || len(sp.Persona.Tools) != 0 {
			t.Fatalf("default session persona = %+v; want the tool-less build persona", sp.Persona)
		}
		belt := tools.NewBelt(effectiveBeltNames(nil, sp.Persona.Tools))
		for _, name := range []string{"bash", "write", "edit"} {
			if !belt.Allows(name) {
				t.Errorf("default belt no longer admits %q", name)
			}
		}
		// An empty Refused list is what keeps appendToolGuidance from
		// adding a belt sentence, so the prompt stays byte-identical.
		if got := belt.Refused(); got != nil {
			t.Errorf("default belt refused %v; the prompt must gain no belt sentence", got)
		}
		if got := belt.Unknown(); got != nil {
			t.Errorf("default belt has unknown names %v", got)
		}
	})
}

// personaBeltRegistry builds a registry over the seam's effective belt
// so admission is asserted against the real enforcement point (registry
// registration), not a reimplementation of it.
func personaBeltRegistry(t *testing.T, configured, personaTools []string) *tools.Registry {
	t.Helper()
	belt := tools.NewBelt(effectiveBeltNames(configured, personaTools))
	reg := tools.NewRegistryWithBelt(belt)
	for _, name := range []string{"read", "grep", "bash", "write", "edit"} {
		reg.Register(tools.NewLeafTool(name))
	}
	return reg
}

// TestPersonaBeltAdmission walks the four combinations through a real
// belted registry: what the effective belt names is what registers,
// and nothing else.
func TestPersonaBeltAdmission(t *testing.T) {
	t.Run("persona allowing write tools admits them absent a belt", func(t *testing.T) {
		reg := personaBeltRegistry(t, nil, []string{"read", "bash"})
		for _, name := range []string{"read", "bash"} {
			if reg.Get(name) == nil {
				t.Errorf("persona-narrowed belt dropped %q", name)
			}
		}
		for _, name := range []string{"write", "edit"} {
			if reg.Get(name) != nil {
				t.Errorf("persona [read bash] admitted %q", name)
			}
		}
	})

	t.Run("configured belt alone keeps today's surface", func(t *testing.T) {
		reg := personaBeltRegistry(t, []string{"read", "grep"}, nil)
		if reg.Get("read") == nil || reg.Get("grep") == nil {
			t.Error("configured belt lost its tools")
		}
		if reg.Get("bash") != nil {
			t.Error("configured belt grew a tool")
		}
	})

	t.Run("both set: intersection only", func(t *testing.T) {
		reg := personaBeltRegistry(t, []string{"read", "grep"}, []string{"read", "bash"})
		if reg.Get("read") == nil {
			t.Error("intersection lost the shared tool")
		}
		for _, name := range []string{"grep", "bash"} {
			if reg.Get(name) != nil {
				t.Errorf("intersection admitted %q — persona narrowing must never widen", name)
			}
		}
	})

	t.Run("neither set: full surface", func(t *testing.T) {
		reg := personaBeltRegistry(t, nil, nil)
		for _, name := range []string{"read", "grep", "bash", "write", "edit"} {
			if reg.Get(name) == nil {
				t.Errorf("full surface lost %q", name)
			}
		}
	})
}

// TestUnknownToolWarnings pins the unknown-name handling at the seam:
// a configured belt name that matches no tool keeps today's warning
// verbatim, and a persona name gets the same treatment — warn and
// ignore — with the persona named as the source. The registered and
// refused fields simulate the belt accounting the registry and the
// gates produce; the helper must not distinguish simulation from the
// real thing.
func TestUnknownToolWarnings(t *testing.T) {
	tests := []struct {
		name         string
		beltNames    []string
		registered   []string // belt.Know — the registry installed them
		refused      []string // belt.Refuse — real tools the belt turned away
		personaName  string
		personaTools []string
		want         []string
	}{
		{
			name:       "configured typo warns with the config wording",
			beltNames:  []string{"read", "ghost"},
			registered: []string{"read"},
			want:       []string{"warning: config `agents.default.tools` names unknown tool \"ghost\"; ignored"},
		},
		{
			name:         "persona-only typo warns with the persona wording",
			beltNames:    []string{"read", "ghost"}, // the persona list IS the belt here
			registered:   []string{"read"},
			personaName:  "planner",
			personaTools: []string{"read", "ghost"},
			want:         []string{"warning: persona \"planner\" names unknown tool \"ghost\"; ignored"},
		},
		{
			name:         "persona typo dropped by the intersection is not lost",
			beltNames:    []string{"read"}, // the intersection of [read] and [read ghost]
			registered:   []string{"read"},
			personaName:  "planner",
			personaTools: []string{"read", "ghost"},
			want:         []string{"warning: persona \"planner\" names unknown tool \"ghost\"; ignored"},
		},
		{
			name:         "persona name on a real tool the belt refuses stays silent here",
			beltNames:    []string{"read"},
			registered:   []string{"read"},
			refused:      []string{"bash"},
			personaName:  "planner",
			personaTools: []string{"read", "bash"},
			want:         nil,
		},
		{
			name:         "gated-off tool the persona names is not a typo",
			beltNames:    []string{"read", "memory_add"},
			registered:   []string{"read", "memory_add"}, // the gate Knows it without a registration
			personaName:  "recaller",
			personaTools: []string{"read", "memory_add"},
			want:         nil,
		},
		{
			name:       "golden: no persona tools warns exactly as today",
			beltNames:  []string{"read", "ghost"},
			registered: []string{"read"},
			want:       []string{"warning: config `agents.default.tools` names unknown tool \"ghost\"; ignored"},
		},
		{
			name: "golden: full surface, nothing configured, no warnings",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			belt := tools.NewBelt(tt.beltNames)
			for _, name := range tt.registered {
				belt.Know(name)
			}
			for _, name := range tt.refused {
				belt.Refuse(name)
			}
			tr := &toolRegistryStage{belt: belt, personaName: tt.personaName, personaTools: tt.personaTools}
			got := unknownToolWarnings(tr)
			if !slices.Equal(got, tt.want) {
				t.Errorf("unknownToolWarnings() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPersonaNarrowing_ReadOnlyStillApplies pins the ordering contract:
// persona narrowing happens first (at registry construction), plan
// mode's read-only level strips after (at loop build, via
// toolsLevelForMode). A persona whose role file allows write tools gets
// them past the belt — and still loses them in plan mode.
func TestPersonaNarrowing_ReadOnlyStillApplies(t *testing.T) {
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(map[string][]byte{
		"builder.md": []byte("---\nname: Builder\nmode: primary\ntools: [read, bash]\ndescription: builds\n---\nbody"),
	}); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	saved := subagent.DefaultRegistry()
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(saved) })

	t.Run("registry persona allowing write tools: belt admits, plan mode strips", func(t *testing.T) {
		sp := resolveSessionPersona("agents.default.agent", "builder")
		if sp.Persona.Name != "builder" || !slices.Equal(sp.Persona.Tools, []string{"read", "bash"}) {
			t.Fatalf("session persona = %+v; want builder with tools [read bash]", sp.Persona)
		}
		belt := tools.NewBelt(effectiveBeltNames(nil, sp.Persona.Tools))
		sessionReg := tools.NewRegistryWithBelt(belt)
		sessionReg.Register(tools.NewLeafTool("read"))
		sessionReg.Register(tools.NewLeafTool("bash"))
		if sessionReg.Get("bash") == nil {
			t.Fatal("belt refused bash; persona narrowing must admit the role file's write tools")
		}
		// On top of the narrowed belt: the plan-mode level.
		s := &agentSession{}
		if got := s.toolsLevelForMode(acpModePlan); got != agent.ReadOnlyTools {
			t.Fatalf("toolsLevelForMode(plan) = %v; want agent.ReadOnlyTools", got)
		}
		// InspectionSetNames is exactly the membership allowsTool checks
		// at the ReadOnlyTools level, so this is the strip.
		if slices.Contains(agent.InspectionSetNames(), "bash") {
			t.Error("plan mode's inspection set admits bash; the read-only level must strip persona-admitted write tools")
		}
	})

	t.Run("plan persona: belt untouched, plan mode strips writes", func(t *testing.T) {
		sp := resolveSessionPersona("agents.default.agent", "plan")
		if !sp.Persona.ReadOnly {
			t.Fatalf("plan persona = %+v; want ReadOnly", sp.Persona)
		}
		if got := effectiveBeltNames(nil, sp.Persona.Tools); got != nil {
			t.Fatalf("plan persona narrowed the belt to %q; it must narrow nothing", got)
		}
		fullBelt := tools.NewBelt(nil)
		if !fullBelt.Allows("bash") {
			t.Fatal("full belt refuses bash; the plan persona's belt must not be the thing that strips")
		}
		s := &agentSession{}
		if got := s.toolsLevelForMode(acpModePlan); got != agent.ReadOnlyTools {
			t.Fatalf("toolsLevelForMode(plan) = %v; want agent.ReadOnlyTools", got)
		}
		if slices.Contains(agent.InspectionSetNames(), "bash") {
			t.Error("plan mode's inspection set admits bash")
		}
	})
}
