package yaah

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
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
			got := resolveSessionPersona("agents.default.agent", tt.configured)

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
		resolveSessionPersona("agents.default.agent", "grunt")
		resolveSessionPersona("agents.default.agent", "ghost")
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
	if got := resolveSessionPersona("agents.default.agent", "plan"); got.Persona.Name != "plan" || got.Fallback {
		t.Errorf("plan without a registry = %+v, want the built-in persona", got)
	}
	if got := resolveSessionPersona("agents.default.agent", "planner"); got.Persona.Name != "build" || !got.Fallback {
		t.Errorf("registry name without a registry = %+v, want fallback to build", got)
	}
	if logged := logs(); !strings.Contains(logged, "falling back") {
		t.Errorf("expected a fallback warning, log:\n%s", logged)
	}
}

// personaResumeFixture persists a session row in a fresh store in the
// shape a resumed session's wiring reads: the row carries the persona the
// session was created with, plus one message so restoreSession accepts it
// as a resume target.
func personaResumeFixture(t *testing.T, id, persona, systemPrompt string) *memory.DB {
	t.Helper()
	db, err := memory.Open(filepath.Join(t.TempDir(), "persona-resume.db"))
	if err != nil {
		t.Fatalf("memory.Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSession(memory.Session{
		ID:           id,
		StartedAt:    time.Now().Unix(),
		Persona:      persona,
		SystemPrompt: systemPrompt,
	}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := db.AddMessage(memory.Message{
		ID: id + "-m0", SessionID: id, Idx: 0, Role: "user", Content: "work", Timestamp: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("AddMessage() error: %v", err)
	}
	return db
}

// personaBudgetTestRegistry installs a registry whose planner persona
// carries budget fields, restoring the previous registry afterwards.
func personaBudgetTestRegistry(t *testing.T) {
	t.Helper()
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(map[string][]byte{
		"planner.md": []byte("---\nname: Planner\nmode: all\ntools: [read]\nmax_iterations: 7\nmax_turns: 5\ndescription: plans changes\n---\nplan body"),
	}); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	saved := subagent.DefaultRegistry()
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(saved) })
}

// resumeConfig is a config whose agents.default.agent names a
// delegate-only persona ("grunt"): if a resume resolution ever read the
// config default instead of the stored name, it would fall back to build
// and every assertion on the stored persona would fail — the changed
// config default is the tripwire.
func resumeConfig() *config.Config {
	return &config.Config{Agent: config.AgentConfig{Default: config.Defaults{Agent: "grunt"}}}
}

// TestResolveSessionPersonaFor_Fresh pins the fresh-session half of the
// persona source chain (yaah-21uk.9): no resume id, the resolution is
// the config default's, exactly as before the persona column existed —
// and no store read happens even when one is handed in.
func TestResolveSessionPersonaFor_Fresh(t *testing.T) {
	personaTestRegistry(t)
	logs := captureSlogWarnings(t)

	cfg := &config.Config{Agent: config.AgentConfig{Default: config.Defaults{Agent: "planner"}}}
	got := resolveSessionPersonaFor(nil, SessionOptions{}, cfg)
	if got.Persona.Name != "planner" || got.Fallback || got.Configured != "planner" {
		t.Fatalf("fresh resolution = %+v; want the config default's planner persona", got)
	}
	if logged := logs(); logged != "" {
		t.Errorf("unexpected warnings:\n%s", logged)
	}
}

// TestResolveSessionPersonaFor_ResumeUsesStoredPersona pins the .9 rule:
// a resumed session resolves its OWN stored persona name, and the config
// default — changed since creation, here to a delegate-only name — cannot
// reach the resolution. Budget and belt ride the same struct a fresh
// session would have resolved, so the effects follow automatically.
func TestResolveSessionPersonaFor_ResumeUsesStoredPersona(t *testing.T) {
	personaBudgetTestRegistry(t)
	db := personaResumeFixture(t, "sess-p", "planner", "")
	logs := captureSlogWarnings(t)

	got := resolveSessionPersonaFor(db, SessionOptions{ResumeSessionID: "sess-p"}, resumeConfig())
	if got.Persona.Name != "planner" || got.Fallback || got.Configured != "planner" {
		t.Fatalf("resume resolution = %+v; want the stored planner persona without fallback", got)
	}
	if len(got.Persona.Tools) != 1 || got.Persona.Tools[0] != "read" {
		t.Errorf("persona tools = %v; want [read] (the belt-shaping input)", got.Persona.Tools)
	}
	b := resolveOrchestratorBudget(&config.Config{}, got.Persona)
	if b.Iterations != 7 || b.Turns != 5 {
		t.Errorf("orchestrator budget = %d/%d; want the stored persona's 7/5", b.Iterations, b.Turns)
	}
	if logged := logs(); logged != "" {
		t.Errorf("unexpected warnings:\n%s", logged)
	}
}

// TestResolveSessionPersonaFor_ResumeMissingStoredPersona: the stored
// persona's role file has been deleted since the session was created.
// resolveSessionPersona's chain handles it — warn, fall back to build —
// and build's effects follow: a zero budget resolving to the config
// defaults unchanged and an empty tools list leaving the belt untouched.
func TestResolveSessionPersonaFor_ResumeMissingStoredPersona(t *testing.T) {
	personaTestRegistry(t)
	db := personaResumeFixture(t, "sess-gone", "ghost", "")
	logs := captureSlogWarnings(t)

	got := resolveSessionPersonaFor(db, SessionOptions{ResumeSessionID: "sess-gone"}, resumeConfig())
	if got.Persona.Name != subagent.BuildPersonaName || !got.Fallback {
		t.Fatalf("resume resolution = %+v; want the build fallback for a missing stored persona", got)
	}
	if logged := logs(); !strings.Contains(logged, "falling back") || !strings.Contains(logged, "configured=ghost") {
		t.Errorf("expected a fallback warning naming the stored persona, log:\n%s", logged)
	}
	if len(got.Persona.Tools) != 0 {
		t.Errorf("build persona tools = %v; want none (belt untouched)", got.Persona.Tools)
	}
	b := resolveOrchestratorBudget(&config.Config{}, got.Persona)
	if b.Iterations != 0 || b.Turns != 0 {
		t.Errorf("build budget = %d/%d; want 0/0 (the zero-budget golden)", b.Iterations, b.Turns)
	}
}

// TestResolveSessionPersonaFor_ResumeLegacyRowIsBuild pins the
// zero-change migration at the resolution site: a row from before the
// persona column stores "", which resolves to build with no warning —
// nothing is missing; pre-persona sessions all ran the plain build
// orchestrator. Reading "" as the config default instead would let a
// later config change rewrite old sessions.
func TestResolveSessionPersonaFor_ResumeLegacyRowIsBuild(t *testing.T) {
	personaTestRegistry(t)
	db := personaResumeFixture(t, "sess-legacy", "", "")
	logs := captureSlogWarnings(t)

	got := resolveSessionPersonaFor(db, SessionOptions{ResumeSessionID: "sess-legacy"}, resumeConfig())
	if got.Persona.Name != subagent.BuildPersonaName || got.Fallback {
		t.Fatalf("legacy resume resolution = %+v; want the plain build persona", got)
	}
	if logged := logs(); logged != "" {
		t.Errorf("unexpected warnings:\n%s", logged)
	}
}

// TestResolveSessionPersonaFor_ResumeUnreadableStore: a store that cannot
// serve the persona read degrades the resume to today's config-default
// resolution with a warning, instead of failing the session — persona
// restore is a fidelity feature, not a gate. The fallback here is the
// config default's own ("grunt" is delegate-only), the session today's
// binary would have built.
func TestResolveSessionPersonaFor_ResumeUnreadableStore(t *testing.T) {
	personaTestRegistry(t)
	db := personaResumeFixture(t, "sess-x", "planner", "")
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	logs := captureSlogWarnings(t)

	got := resolveSessionPersonaFor(db, SessionOptions{ResumeSessionID: "sess-x"}, resumeConfig())
	if got.Persona.Name != subagent.BuildPersonaName || !got.Fallback {
		t.Fatalf("unreadable-store resolution = %+v; want the config default's build fallback", got)
	}
	if logged := logs(); !strings.Contains(logged, "stored persona failed") {
		t.Errorf("expected a warning about the failed persona read, log:\n%s", logged)
	}
}

// TestRestoreTranscriptPersistsPersonaAtCreation pins the write half of
// yaah-21uk.9 through the real creation site: a fresh session's row
// carries the resolved persona's name, so a later resume can restore
// budget and belt from the session's own selection.
func TestRestoreTranscriptPersistsPersonaAtCreation(t *testing.T) {
	personaTestRegistry(t)

	tests := []struct {
		name        string
		configured  string
		wantPersona string
	}{
		{"configured persona persists", "planner", "planner"},
		{"unset config persists build", "", subagent.BuildPersonaName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := memory.Open(filepath.Join(t.TempDir(), "create.db"))
			if err != nil {
				t.Fatalf("memory.Open() error: %v", err)
			}
			t.Cleanup(func() { db.Close() })

			cfg := &config.Config{Agent: config.AgentConfig{Default: config.Defaults{Agent: tt.configured}}}
			lc := &sessionConfig{
				modelName: "test-model",
				persona:   resolveSessionPersona("agents.default.agent", cfg.Agent.Default.Agent),
			}
			st := &loopInfraStage{}
			tr := &toolRegistryStage{db: db}
			mcps := &mcpSkillsStage{systemPrompt: "ASSEMBLED PROMPT", identityEnd: 3}

			if err := restoreTranscript(st, cfg, SessionOptions{}, lc, &workspaceStage{cwd: t.TempDir()}, tr, mcps); err != nil {
				t.Fatalf("restoreTranscript() error: %v", err)
			}
			if st.sessionID == "" {
				t.Fatal("restoreTranscript produced no session id")
			}
			row, err := db.GetSession(st.sessionID)
			if err != nil {
				t.Fatalf("GetSession(%q) error: %v", st.sessionID, err)
			}
			if row.Persona != tt.wantPersona {
				t.Fatalf("created row persona = %q; want %q (the resolved persona persisted at creation)", row.Persona, tt.wantPersona)
			}
			if row.SystemPrompt != "ASSEMBLED PROMPT" {
				t.Errorf("created row system prompt = %q; want the assembled prompt", row.SystemPrompt)
			}
		})
	}
}

// TestResumeKeepsStoredPromptBytes pins the byte-stability half of
// yaah-21uk.9: resume restores the persisted SystemPrompt verbatim even
// though the wiring re-resolves the persona (whose body could have
// changed, or whose fallback would assemble differently) — the restored
// name drives budget and belt only, never the prompt.
func TestResumeKeepsStoredPromptBytes(t *testing.T) {
	personaBudgetTestRegistry(t)
	const stored = "IDENTITY\n\nPLANNER PERSONA LAYER\n\nORIGINAL TOOL GUIDANCE"
	db := personaResumeFixture(t, "sess-bytes", "planner", stored)

	// The prompt a fresh assembly would produce under the current
	// config: byte-different from the stored one.
	const reassembled = "REASSEMBLED UNDER TODAY'S CONFIG"

	restored, err := restoreSession(db, "sess-bytes", reassembled)
	if err != nil {
		t.Fatalf("restoreSession() error: %v", err)
	}
	if restored.SystemPrompt != stored {
		t.Fatalf("resumed SystemPrompt = %q; want the stored bytes verbatim (resume must not rebuild the prompt from the re-resolved persona)", restored.SystemPrompt)
	}
}

// TestSetPersonaPreservesToolGuidanceSuffix pins review F1: a fresh
// session's live mainPrompt carries the tool-guidance blocks
// appendToolGuidance appended at construction; a mid-session switch
// rebuilds the base from the retained layers and must RE-APPLY the
// suffix, not silently drop it.
func TestSetPersonaPreservesToolGuidanceSuffix(t *testing.T) {
	personaTestRegistry(t)

	const guidance = "\n\n## Sub-agents\nDelegate independent subtasks to specialist sub-agents."
	build, _ := subagent.ResolvePersona(subagent.BuildPersonaName)

	layers := prompts.Layers{Identity: prompts.IdentityPrompt}
	sys, end := prompts.BuildMarked(layers)
	s := &agentSession{cfg: &config.Config{}}
	s.promptLayers = layers
	s.systemPrompt = sys
	s.identityEnd = end
	s.persona = build
	base := buildMainPrompt(s.cfg, SessionOptions{}, sys, end)
	s.mainPrompt = base + guidance
	s.promptGuidance = guidance
	s.SetView(&safeCaptureView{})

	if err := s.SetPersona("planner"); err != nil {
		t.Fatalf("SetPersona(planner): %v", err)
	}
	if !strings.Contains(s.mainPrompt, guidance) {
		t.Errorf("switch dropped the tool-guidance suffix from mainPrompt:\n%s", s.mainPrompt)
	}
	if !strings.HasSuffix(s.mainPrompt, guidance) {
		t.Errorf("guidance must remain the prompt's tail after the rebuilt base:\n%s", s.mainPrompt)
	}
	if !strings.Contains(s.systemPrompt, "plan body") {
		t.Errorf("switch did not rebind the persona body into systemPrompt")
	}
}
