package yaah

import (
	"slices"
	"testing"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/permissions"
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

// personaBeltRegistry builds a registry over the effective belt the
// construction wiring feeds: config-only since the switch (yaah-21uk.12)
// — buildToolRegistry passes persona=nil to effectiveBeltNames — so
// admission is asserted against the real enforcement point (registry
// registration) for the belt that remains construction-time.
func personaBeltRegistry(t *testing.T, configured []string) *tools.Registry {
	t.Helper()
	belt := tools.NewBelt(effectiveBeltNames(configured, nil))
	reg := tools.NewRegistryWithBelt(belt)
	for _, name := range []string{"read", "grep", "bash", "write", "edit"} {
		reg.Register(tools.NewLeafTool(name))
	}
	return reg
}

// TestPersonaBeltAdmission pins the belt half of the seam: the
// construction registry admits exactly the configured belt's surface.
// Persona narrowing no longer rides this belt — a persona's tools list
// intersects the ADVERTISED set at loop build (TestPersonaToolsSeam in
// persona_switch_test.go, internal/agent's TestPersonaToolsNarrowAdvertisedSet)
// so a mid-session switch re-narrows without registry surgery.
func TestPersonaBeltAdmission(t *testing.T) {
	t.Run("configured belt alone shapes the registry", func(t *testing.T) {
		reg := personaBeltRegistry(t, []string{"read", "grep"})
		if reg.Get("read") == nil || reg.Get("grep") == nil {
			t.Error("configured belt lost its tools")
		}
		for _, name := range []string{"bash", "write", "edit"} {
			if reg.Get(name) != nil {
				t.Errorf("configured belt grew %q", name)
			}
		}
	})

	t.Run("no configured belt: full surface", func(t *testing.T) {
		reg := personaBeltRegistry(t, nil)
		for _, name := range []string{"read", "grep", "bash", "write", "edit"} {
			if reg.Get(name) == nil {
				t.Errorf("full surface lost %q", name)
			}
		}
	})

	t.Run("persona tools do not shape the construction belt", func(t *testing.T) {
		// The registry a persona-carrying session builds is the same
		// config-only surface: narrowing happens per run, not at
		// registration. This is the pin that keeps a switch cheap.
		sp := resolveSessionPersona("agents.default.agent", "plan")
		if len(sp.Persona.Tools) != 0 {
			t.Fatalf("plan persona carries tools %v; it must narrow nothing", sp.Persona.Tools)
		}
		reg := personaBeltRegistry(t, nil)
		if reg.Get("bash") == nil {
			t.Error("a persona-carrying session must still register the full config surface")
		}
	})
}

// TestUnknownToolWarnings pins the unknown-name handling at the seam:
// a configured belt name that matches no tool keeps today's warning
// verbatim, and a persona name gets the same treatment — warn and
// ignore — with the persona named as the source. Since the belt went
// config-only (yaah-21uk.12), persona names are checked against the
// REGISTRY, not the belt: the helper registers a real registry so the
// lookup is exercised, and belt.Refuse marks a real tool the config
// belt turned away (known, never a typo).
func TestUnknownToolWarnings(t *testing.T) {
	tests := []struct {
		name         string
		beltNames    []string
		registered   []string // belt.Know + a real registration
		knownOnly    []string // belt.Know only — gated/runtime-wired tools
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
			beltNames:    []string{"read"},
			registered:   []string{"read"},
			personaName:  "planner",
			personaTools: []string{"read", "ghost"},
			want:         []string{"warning: persona \"planner\" names unknown tool \"ghost\"; ignored"},
		},
		{
			name:         "persona typo with no configured belt still warns",
			beltNames:    nil, // allow-all belt: the registry is the only oracle
			registered:   []string{"read"},
			personaName:  "planner",
			personaTools: []string{"read", "ghost"},
			want:         []string{"warning: persona \"planner\" names unknown tool \"ghost\"; ignored"},
		},
		{
			name:         "persona name on a real tool the session holds stays silent",
			beltNames:    []string{"read", "bash"},
			registered:   []string{"read", "bash"},
			personaName:  "planner",
			personaTools: []string{"read", "bash"},
			want:         nil,
		},
		{
			name:       "real tool the persona names is not a typo",
			beltNames:  []string{"read", "memory_add"},
			registered: []string{"read"},
			// memory_add is runtime-wired, never a leaf tool: the belt
			// knows it (gate admission) without a registry registration.
			// Known-but-unheld is exactly the unavailable class the
			// curated default created (yaah-6mi9.1): the persona's
			// narrowing drops it, so the warning says so instead of
			// staying silent.
			knownOnly:    []string{"memory_add"},
			personaName:  "recaller",
			personaTools: []string{"read", "memory_add"},
			want:         []string{"warning: persona \"recaller\" names tool \"memory_add\" that is not available in this session (withheld from the default belt, or its lane is gated off here); narrowed away"},
		},
		{
			name:       "persona naming a tool withheld by the default belt warns, not silences",
			beltNames:  []string{"read"},
			registered: []string{"read"},
			// copy is belt-refused (it is in removedFromOrchestratorDefault),
			// so it is known without being held: the persona loses it
			// silently unless this warns.
			refused:      []string{"copy"},
			personaName:  "mover",
			personaTools: []string{"read", "copy"},
			want:         []string{"warning: persona \"mover\" names tool \"copy\" that is not available in this session (withheld from the default belt, or its lane is gated off here); narrowed away"},
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
			// An empty registry, like the belt-curated session: the held
			// set is exactly what the case registered, so known-but-
			// unheld (the unavailable class) is distinguishable here.
			reg := tools.NewEmptyRegistry()
			for _, name := range tt.registered {
				belt.Know(name)
				reg.Register(tools.NewLeafTool(name))
			}
			for _, name := range tt.knownOnly {
				belt.Know(name)
			}
			for _, name := range tt.refused {
				belt.Refuse(name)
			}
			tr := &toolRegistryStage{belt: belt, reg: reg, personaName: tt.personaName, personaTools: tt.personaTools}
			got := unknownToolWarnings(tr)
			if !slices.Equal(got, tt.want) {
				t.Errorf("unknownToolWarnings() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPersonaNarrowing_PerRunLoopSeam pins the ordering contract after
// the switch (yaah-21uk.12): the construction belt is config-only, the
// persona's tools list narrows the ADVERTISED set at loop build (so a
// switch re-narrows), and plan mode's read-only level still strips on
// top. A ReadOnly persona gates the level even in build mode — the
// transition seam part 2 retires.
func TestPersonaNarrowing_PerRunLoopSeam(t *testing.T) {
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(map[string][]byte{
		"builder.md": []byte("---\nname: Builder\nmode: primary\ntools: [read, bash]\ndescription: builds\n---\nbody"),
	}); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	saved := subagent.DefaultRegistry()
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(saved) })

	t.Run("registry persona allowing write tools: belt admits, per-run seam narrows", func(t *testing.T) {
		sp := resolveSessionPersona("agents.default.agent", "builder")
		if sp.Persona.Name != "builder" || !slices.Equal(sp.Persona.Tools, []string{"read", "bash"}) {
			t.Fatalf("session persona = %+v; want builder with tools [read bash]", sp.Persona)
		}
		// Construction: the config-only belt admits the role file's write
		// tools — narrowing no longer happens at registration.
		belt := tools.NewBelt(effectiveBeltNames(nil, nil))
		sessionReg := tools.NewRegistryWithBelt(belt)
		sessionReg.Register(tools.NewLeafTool("read"))
		sessionReg.Register(tools.NewLeafTool("bash"))
		if sessionReg.Get("bash") == nil {
			t.Fatal("belt refused bash; the construction belt is config-only and must admit")
		}
		// The next loop build narrows the advertised set instead.
		s := &agentSession{cfg: &config.Config{}, persona: sp.Persona}
		b := s.loopBuilder(nil, "test-model", nil, "", nil, "", "")
		if !slices.Equal(b.PersonaTools, []string{"read", "bash"}) {
			t.Errorf("builder PersonaTools = %v; want [read bash]", b.PersonaTools)
		}
		// A write-capable persona gets the full level — the read-only
		// strip belongs to read-only personas (the plan-persona subtest
		// pins it); this one only narrows the advertised set.
		if got := sessionToolsLevel(s.readOnly); got != agent.FullTools {
			t.Fatalf("effectiveToolsLevel() = %v; want agent.FullTools", got)
		}
	})

	t.Run("switch re-narrows the advertised set without registry surgery", func(t *testing.T) {
		buildPersona, ok := subagent.ResolvePersona(subagent.BuildPersonaName)
		if !ok {
			t.Fatal("build persona did not resolve")
		}
		s := &agentSession{cfg: &config.Config{}, persona: buildPersona}
		b := s.loopBuilder(nil, "test-model", nil, "", nil, "", "")
		if len(b.PersonaTools) != 0 {
			t.Fatalf("build persona PersonaTools = %v; want no narrowing", b.PersonaTools)
		}
		builderPersona, ok := subagent.ResolvePersona("builder")
		if !ok {
			t.Fatal("builder persona did not resolve")
		}
		s.mu.Lock()
		s.persona = builderPersona
		s.mu.Unlock()
		b = s.loopBuilder(nil, "test-model", nil, "", nil, "", "")
		if !slices.Equal(b.PersonaTools, []string{"read", "bash"}) {
			t.Errorf("after the switch PersonaTools = %v; want [read bash]", b.PersonaTools)
		}
	})

	t.Run("plan persona: belt untouched, read-only is the session's call", func(t *testing.T) {
		sp := resolveSessionPersona("agents.default.agent", "plan")
		if got := effectiveBeltNames(nil, sp.Persona.Tools); got != nil {
			t.Fatalf("plan persona narrowed the belt to %q; it must narrow nothing", got)
		}
		// The persona carries no mutability: in a full session the plan
		// persona gets the full level; read-only is the session's mode
		// axis (decouple-persona-mode-approval).
		s := &agentSession{cfg: &config.Config{}, persona: sp.Persona}
		if got := sessionToolsLevel(s.readOnly); got != agent.FullTools {
			t.Errorf("effectiveToolsLevel() on the plan persona in a full session = %v; want agent.FullTools", got)
		}
		s.readOnly = true
		if got := sessionToolsLevel(s.readOnly); got != agent.ReadOnlyTools {
			t.Errorf("effectiveToolsLevel() in a read-only session = %v; want agent.ReadOnlyTools", got)
		}
		if slices.Contains(agent.InspectionSetNames(), "bash") {
			t.Error("read-only mode's inspection set admits bash")
		}
	})
}

// TestMergePermissions pins the composition order (parity W5.4): global
// config rules first, persona rules appended after, so under
// last-match-wins the persona's rule wins wherever both name the same
// tool-and-resource — and neither side alone is disturbed.
func TestMergePermissions(t *testing.T) {
	global := permissions.Rules{{Tool: "edit", Resource: "*", Effect: permissions.Deny}}
	persona := permissions.Rules{{Tool: "edit", Resource: "*.md", Effect: permissions.Allow}}

	merged := mergePermissions(global, persona)
	if len(merged) != 2 {
		t.Fatalf("merged %d rules, want 2", len(merged))
	}
	// The persona's later rule wins on the overlap.
	if effect, ok := merged.Match("edit", "notes.md"); !ok || effect != permissions.Allow {
		t.Errorf("persona rule must win on overlap: got (%v, %v)", effect, ok)
	}
	if effect, ok := merged.Match("edit", "main.go"); !ok || effect != permissions.Deny {
		t.Errorf("global rule still governs non-overlap: got (%v, %v)", effect, ok)
	}

	// One-sided rule sets pass through without copying.
	if got := mergePermissions(nil, persona); len(got) != 1 || got[0] != persona[0] {
		t.Errorf("persona-only merge = %+v", got)
	}
	if got := mergePermissions(global, nil); len(got) != 1 || got[0] != global[0] {
		t.Errorf("global-only merge = %+v", got)
	}
}
