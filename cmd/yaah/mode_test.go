package yaah

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
)

// The tool gate derives from the persona alone since part 2: a read-only
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

// Golden byte-stability (part 2): with no persona change, the per-run
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

// The deprecated SetMode wrapper maps the mode name onto the persona of the
// same name, announces via AgentChangedEvent (ModeChangedEvent is gone), and
// touches neither the persona column semantics nor the mode column: the
// persona is the only persisted axis.
func TestSetModeMapsOntoPersona(t *testing.T) {
	sess := &agentSession{}
	view := &safeCaptureView{}
	sess.SetView(view)

	if err := sess.SetMode(acpModePlan); err != nil {
		t.Fatalf("SetMode(%q) = %v", acpModePlan, err)
	}
	if got := sess.PersonaName(); got != subagent.PlanPersonaName {
		t.Fatalf("PersonaName() = %q; want %q", got, subagent.PlanPersonaName)
	}
	if got := sess.Mode(); got != acpModePlan {
		t.Fatalf("Mode() = %q; want %q (derived from the persona)", got, acpModePlan)
	}
	if len(view.events) != 1 {
		t.Fatalf("view received %d event(s) on a mode-alias switch, want 1", len(view.events))
	}
	evt, ok := view.events[0].(*agent.AgentChangedEvent)
	if !ok {
		t.Fatalf("dispatched event = %T; want *agent.AgentChangedEvent", view.events[0])
	}
	if evt.Persona != subagent.PlanPersonaName {
		t.Errorf("event Persona = %q; want %q", evt.Persona, subagent.PlanPersonaName)
	}
	for _, want := range []string{"persona: plan", "withheld", "deprecat", "/agent"} {
		if !strings.Contains(evt.Notice(), want) {
			t.Errorf("plan notice %q missing %q", evt.Notice(), want)
		}
	}

	// Switching back works and re-derives build.
	if err := sess.SetMode(acpModeBuild); err != nil {
		t.Fatalf("SetMode(%q) = %v", acpModeBuild, err)
	}
	if got := sess.PersonaName(); got != subagent.BuildPersonaName {
		t.Fatalf("PersonaName() = %q after switching back; want %q", got, subagent.BuildPersonaName)
	}
	if got := sess.Mode(); got != acpModeBuild {
		t.Fatalf("Mode() = %q after switching back; want %q", got, acpModeBuild)
	}
}

// An unknown mode is still refused, changes nothing, and announces nothing.
func TestSetModeRejectsUnknownModeWithoutNotice(t *testing.T) {
	sess := &agentSession{}
	view := &safeCaptureView{}
	sess.SetView(view)

	if err := sess.SetMode("yolo"); err == nil {
		t.Fatalf("SetMode(%q) = nil; want an error", "yolo")
	}
	if len(view.events) != 0 {
		t.Fatalf("view received %d event(s) for a refused mode, want 0", len(view.events))
	}
	if got := sess.Mode(); got != acpModeBuild {
		t.Errorf("Mode() = %q after a refused switch; want %q", got, acpModeBuild)
	}
}

// Views that must not be written between runs (REPL, ACP) get the notice when
// the next run drains the buffer, so it still appears in-band.
func TestSetModeNoticeBuffersForUnsafeView(t *testing.T) {
	sess := &agentSession{}
	view := &captureView{}
	sess.SetView(view)

	if err := sess.SetMode(acpModePlan); err != nil {
		t.Fatalf("SetMode(%q) = %v", acpModePlan, err)
	}
	if len(view.events) != 0 {
		t.Fatalf("unsafe view received %d event(s) between runs, want 0", len(view.events))
	}
	sess.deliverPendingEvents()
	if len(view.events) != 1 {
		t.Fatalf("view received %d event(s) after drain, want 1", len(view.events))
	}
	if _, ok := view.events[0].(*agent.AgentChangedEvent); !ok {
		t.Fatalf("delivered event = %T; want *agent.AgentChangedEvent", view.events[0])
	}
}

// The alias path must NOT write the mode column: the persona column is the
// truth, and a legacy mode value left in the row never re-narrows a session
// the persona has widened.
func TestSetModeLeavesModeColumnUntouched(t *testing.T) {
	db, err := memory.Open(filepath.Join(t.TempDir(), "mode.db"))
	if err != nil {
		t.Fatalf("memory.Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSession(memory.Session{ID: "sess-mode", StartedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	sess := &agentSession{db: db, sessionID: "sess-mode"}

	if err := sess.SetMode(acpModePlan); err != nil {
		t.Fatalf("SetMode(%q) = %v", acpModePlan, err)
	}
	if got, err := db.SessionPersona("sess-mode"); err != nil || got != subagent.PlanPersonaName {
		t.Fatalf("persisted persona = %q (err %v); want %q", got, err, subagent.PlanPersonaName)
	}
	if got, err := db.SessionMode("sess-mode"); err != nil || got != "" {
		t.Fatalf("persisted mode = %q (err %v); want \"\" — the persona column is the only written axis", got, err)
	}

	// Switching back persists the persona, still without a mode write.
	if err := sess.SetMode(acpModeBuild); err != nil {
		t.Fatalf("SetMode(%q) = %v", acpModeBuild, err)
	}
	if got, err := db.SessionPersona("sess-mode"); err != nil || got != subagent.BuildPersonaName {
		t.Fatalf("persisted persona = %q (err %v); want %q", got, err, subagent.BuildPersonaName)
	}
}

// A session with no store still switches: persistence is bookkeeping on top of
// the switch, never a precondition for it.
func TestSetModeWithoutStoreStillSwitches(t *testing.T) {
	sess := &agentSession{}

	if err := sess.SetMode(acpModePlan); err != nil {
		t.Fatalf("SetMode(%q) = %v", acpModePlan, err)
	}
	if got := sess.Mode(); got != acpModePlan {
		t.Fatalf("Mode() = %q; want %q", got, acpModePlan)
	}
}

// Legacy resume migration (part 2): a row with an empty persona column and
// mode==plan was narrowed by the retired plan mode, so resume resolves the
// plan persona. Every other legacy shape — empty mode, build mode, junk from
// a newer binary — reads as build, the pre-persona default.
func TestStoredPersonaName_MigratesLegacyPlanMode(t *testing.T) {
	db, err := memory.Open(filepath.Join(t.TempDir(), "migrate.db"))
	if err != nil {
		t.Fatalf("memory.Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range []string{"legacy-plan", "legacy-build", "legacy-empty", "legacy-junk", "modern-plan"} {
		if err := db.CreateSession(memory.Session{ID: id, StartedAt: time.Now().Unix()}); err != nil {
			t.Fatalf("CreateSession(%s) error: %v", id, err)
		}
	}
	if err := db.SetSessionMode("legacy-plan", acpModePlan); err != nil {
		t.Fatalf("SetSessionMode() error: %v", err)
	}
	if err := db.SetSessionMode("legacy-build", acpModeBuild); err != nil {
		t.Fatalf("SetSessionMode() error: %v", err)
	}
	if err := db.SetSessionMode("legacy-junk", "yolo"); err != nil {
		t.Fatalf("SetSessionMode() error: %v", err)
	}
	if err := db.SetSessionPersona("modern-plan", subagent.PlanPersonaName); err != nil {
		t.Fatalf("SetSessionPersona() error: %v", err)
	}

	cases := []struct {
		id   string
		want string
	}{
		{"legacy-plan", subagent.PlanPersonaName},
		{"legacy-build", ""},
		{"legacy-empty", ""},
		{"legacy-junk", ""},
		{"modern-plan", subagent.PlanPersonaName},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			if got := storedPersonaName(db, tc.id, "fallback"); got != tc.want {
				t.Fatalf("storedPersonaName(%q) = %q; want %q", tc.id, got, tc.want)
			}
		})
	}
}

// The migration must reach the real resume selection: resolveSessionPersonaFor
// over a legacy plan-mode row yields the plan persona, so a resumed session
// comes back narrowed with the read-only gate.
func TestResolveSessionPersonaFor_MigratesLegacyPlanRow(t *testing.T) {
	db, err := memory.Open(filepath.Join(t.TempDir(), "migrate.db"))
	if err != nil {
		t.Fatalf("memory.Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	const id = "legacy-plan"
	if err := db.CreateSession(memory.Session{ID: id, StartedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := db.SetSessionMode(id, acpModePlan); err != nil {
		t.Fatalf("SetSessionMode() error: %v", err)
	}

	opts := SessionOptions{ResumeSessionID: id}
	cfg := &config.Config{}
	got := resolveSessionPersonaFor(db, opts, cfg)
	if got.Persona.Name != subagent.PlanPersonaName {
		t.Fatalf("resume persona = %q; want %q", got.Persona.Name, subagent.PlanPersonaName)
	}
	if !got.Persona.ReadOnly {
		t.Fatal("migrated plan persona is not ReadOnly; the resumed session would get the full toolset")
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
