package yaah

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
)

func TestSubAgentDispatchLine_UnknownProviderFallsBack(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{"zai": {}},
		Agent: config.AgentConfig{
			Default: config.Defaults{Provider: "zai", Model: "GLM-5.3"},
			SubAgent: config.SubAgentConfig{
				Provider: "typo",
				Model:    "GLM-5.3-Flash",
			},
		},
	}

	got := subAgentDispatchLine(cfg)
	if !strings.Contains(got, "provider=zai") {
		t.Errorf("dangling provider should fall back to the planner: %s", got)
	}
	if strings.Contains(got, "typo") {
		t.Errorf("dispatch line must not name an unusable provider: %s", got)
	}
	if !strings.Contains(got, "model=GLM-5.3") || strings.Contains(got, "GLM-5.3-Flash") {
		t.Errorf("dangling provider must also drop the sub-agent model override: %s", got)
	}
}

func TestSubAgentDispatchLine(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{"zai": {MaxTokens: 16384}},
		Agent: config.AgentConfig{
			Default: config.Defaults{Provider: "zai", Model: "GLM-5.3"},
			SubAgent: config.SubAgentConfig{
				Model:          "GLM-5.3-Flash",
				MaxConcurrency: 5,
				DefaultTimeout: 600,
			},
		},
	}

	got := subAgentDispatchLine(cfg)
	for _, want := range []string{
		"provider=zai",
		"model=GLM-5.3-Flash",
		"max_concurrency=5",
		"default_timeout=600s",
		"max_tokens=16384",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dispatch line missing %q: %s", want, got)
		}
	}
}

func TestBuildSubAgentBasePrompt_LeanIdentity(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("Project rule: prefer table-driven tests."), 0o644); err != nil {
		t.Fatal(err)
	}

	got := buildSubAgentBasePrompt(cwd, nil)

	if !strings.Contains(got, "You are a yaah sub-agent") {
		t.Errorf("missing sub-agent identity:\n%s", got)
	}
	// These live in the orchestrator identity; the sub-agent base must never
	// embed it, whatever the builder wiring looks like. A tool name would be a
	// weak marker here — the base prompt embeds AGENTS.md and memory facts, so
	// any common English word ("delegate") can appear legitimately. The
	// orchestrator-only guidance sentence is the distinctive thing to ban.
	for _, banned := range []string{
		prompts.IdentityPrompt,
		"most expensive resource",
		"list_subagents",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("sub-agent base must not contain %q:\n%s", banned, got)
		}
	}
	if !strings.Contains(got, "Project rule: prefer table-driven tests.") {
		t.Errorf("sub-agent base must carry project AGENTS.md content:\n%s", got)
	}
}

func TestBuildSubAgentBasePrompt_CarriesUserContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("User rule: no emoji in responses."), 0o644); err != nil {
		t.Fatal(err)
	}

	got := buildSubAgentBasePrompt(t.TempDir(), nil)

	if !strings.Contains(got, "User rule: no emoji in responses.") {
		t.Errorf("sub-agent base must carry user AGENTS.md content:\n%s", got)
	}
}

func TestBuildSubAgentBasePrompt_MemoryFactsWithoutToolGuidelines(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	db, err := memory.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.AddMemory(memory.Entry{
		Text: "the widget config lives in configs/widget.yaml",
		Tags: `["project:probe"]`,
	}); err != nil {
		t.Fatal(err)
	}

	got := buildSubAgentBasePrompt(t.TempDir(), db)

	if !strings.Contains(got, "the widget config lives in configs/widget.yaml") {
		t.Errorf("sub-agent base must carry stored memory facts:\n%s", got)
	}
	// The main-session builder appends memory-tool guidelines when the
	// session registered the memory tools; the sub-agent base must carry
	// the facts but not the guidelines, since no sub-agent role has memory
	// tools.
	for _, banned := range []string{"Memory Guidelines", "memory_add", "memory_search"} {
		if strings.Contains(got, banned) {
			t.Errorf("sub-agent base must not contain memory-tool guidance %q:\n%s", banned, got)
		}
	}
}

// TestBuildSystemPrompt_MemoryGuidelinesGatedOnTools pins the invariant
// the wiring relies on: the Memory Guidelines block advertises exactly the
// memory tools the session registered. A one-shot session against an empty
// store gets neither half, so the prompt cannot tell the model to call
// memory_add in a run that has no memory tools — and pays no tokens for
// the block.
func TestBuildSystemPrompt_MemoryGuidelinesGatedOnTools(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	cfg := &config.Config{
		Providers: map[string]config.Provider{"zai": {}},
		Agent: config.AgentConfig{
			Default: config.Defaults{Provider: "zai", Model: "GLM-5.3"},
		},
	}
	dir := t.TempDir()

	full := prompts.MemoryGuidance{Recall: true, Writers: true}
	withTools := buildSystemPrompt(cfg, dir, nil, "", full)
	if !strings.Contains(withTools, "## Memory Guidelines") {
		t.Error("a session with memory tools must carry the Memory Guidelines block")
	}
	for _, want := range []string{"memory_search", "memory_add", "memory_promote"} {
		if !strings.Contains(withTools, want) {
			t.Errorf("full guidance must name %q", want)
		}
	}
	if strings.Contains(withTools, "cannot save") {
		t.Error("a session with the writers must not be told it cannot save")
	}

	none := prompts.MemoryGuidance{}
	withoutTools := buildSystemPrompt(cfg, dir, nil, "", none)
	for _, banned := range []string{"## Memory Guidelines", "memory_add", "memory_search", "memory_promote"} {
		if strings.Contains(withoutTools, banned) {
			t.Errorf("a session without memory tools must not advertise %q", banned)
		}
	}

	// A resumed session never gets the guidelines even with the tools
	// registered — the pre-existing rule, unchanged by the gate.
	resumed := buildSystemPrompt(cfg, dir, nil, "sess-123", full)
	if strings.Contains(resumed, "## Memory Guidelines") {
		t.Error("a resumed session must not re-append the Memory Guidelines block")
	}
}

// TestBuildSystemPrompt_MemoryGuidelinesRecallOnly pins the yaah-zx18 split:
// a one-shot run against a populated store keeps the readers, so the prompt
// carries the recall bullets and the "cannot save" boundary — and names no
// writer tool, because none is in the request.
func TestBuildSystemPrompt_MemoryGuidelinesRecallOnly(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	cfg := &config.Config{
		Providers: map[string]config.Provider{"zai": {}},
		Agent: config.AgentConfig{
			Default: config.Defaults{Provider: "zai", Model: "GLM-5.3"},
		},
	}

	got := buildSystemPrompt(cfg, t.TempDir(), nil, "", prompts.MemoryGuidance{Recall: true})

	if !strings.Contains(got, "## Memory Guidelines") {
		t.Fatal("a recall-only session still needs the guidelines heading")
	}
	for _, want := range []string{"memory_search", "memory_search_sessions"} {
		if !strings.Contains(got, want) {
			t.Errorf("recall-only guidance must name %q", want)
		}
	}
	for _, banned := range []string{"memory_add", "memory_update", "memory_delete", "memory_promote"} {
		if strings.Contains(got, banned) {
			t.Errorf("recall-only guidance must not advertise %q", banned)
		}
	}
	// The model is told the boundary rather than left to discover it by
	// calling a writer that is not there.
	if !strings.Contains(got, "cannot save") {
		t.Error("a recall-only session must be told it cannot persist")
	}
}

// TestStoreHasRecall pins the probe that decides whether a one-shot session
// keeps the read-only memory tools. An empty store has nothing to recall, so
// the readers are dead weight there — and that is what preserves the measured
// benchmark floor, since a bench container starts with a fresh YAAH_HOME.
func TestStoreHasRecall(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	if storeHasRecall(nil) {
		t.Error("a nil database has nothing to recall")
	}

	db, err := memory.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	if storeHasRecall(db) {
		t.Error("a fresh store has nothing to recall; this is the case the bench floor depends on")
	}
	if err := db.AddMemory(memory.Entry{Text: "the widget config lives in configs/widget.yaml", Tags: `["project:probe"]`}); err != nil {
		t.Fatal(err)
	}
	if !storeHasRecall(db) {
		t.Error("a store holding a memory entry has something to recall")
	}
	db.Close()

	// memory_search_sessions searches the messages table, not the memory
	// one. A session ROW is not enough: wiring creates this session's own
	// row before any turn runs, so counting rows would make the probe
	// permanently true for any YAAH_HOME that had ever been used — and a row
	// with no messages recalls nothing, since the tool skips message-less
	// sessions. This is the case the empty-store gate depends on.
	sess, err := memory.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.CreateSession(memory.Session{ID: "sess-1", StartedAt: 1, CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if storeHasRecall(sess) {
		t.Error("a session row with no messages recalls nothing; counting rows would leave the gate permanently open after any first run")
	}
	if err := sess.AddMessage(memory.Message{SessionID: "sess-1", Idx: 0, Role: "user", Content: "where does the widget config live?", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	if !storeHasRecall(sess) {
		t.Error("a store holding a past transcript has something to recall")
	}
}
