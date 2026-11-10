package yaah

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/tools"
)

// TestRestoreMessage_carriesReasoningSignature pins that resume does not drop
// the thinking signature. A thinking block is only re-emitted when both its
// content and its signature are present, so losing the signature here makes
// replay silently stop working after `yaah resume` even though the column
// was loaded from the DB.
func TestRestoreMessage_carriesReasoningSignature(t *testing.T) {
	got := restoreMessage(memory.Message{
		Role:               "assistant",
		Content:            "answer",
		ReasoningContent:   "because",
		ReasoningSignature: "sig-1",
		ToolName:           "bash",
		ToolCallID:         "call-1",
	})

	if got.ReasoningContent != "because" {
		t.Errorf("ReasoningContent = %q", got.ReasoningContent)
	}
	if got.ReasoningSignature != "sig-1" {
		t.Errorf("ReasoningSignature = %q, want %q", got.ReasoningSignature, "sig-1")
	}
	if got.Name != "bash" || got.ToolCallID != "call-1" {
		t.Errorf("tool identity lost: name=%q id=%q", got.Name, got.ToolCallID)
	}
}

// newResumeFixture persists a session with one message, so restore's
// "no messages" guard does not fire.
func newResumeFixture(t *testing.T, id string) *memory.DB {
	t.Helper()
	db, err := memory.Open(filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatalf("memory.Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().Unix()
	if err := db.CreateSession(memory.Session{ID: id, StartedAt: now}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := db.AddMessage(memory.Message{
		ID: "msg-1", SessionID: id, Idx: 0, Role: "user", Content: "plan this work", Timestamp: now,
	}); err != nil {
		t.Fatalf("AddMessage() error: %v", err)
	}
	return db
}

// A resumed session keeps its transcript and system-prompt restore semantics;
// the legacy mode column is no longer read here (the persona restore path in
// resolveSessionPersonaFor/storedPersonaName owns the migration — see
// TestStoredPersonaName_MigratesLegacyPlanMode in mode_test.go).
func TestRestoreSession_RecoversTranscript(t *testing.T) {
	const id = "sess-plan"
	db := newResumeFixture(t, id)

	got, err := restoreSession(db, id, "BASE PROMPT")
	if err != nil {
		t.Fatalf("restoreSession() error: %v", err)
	}
	if got.SessionID != id {
		t.Fatalf("restored SessionID = %q; want %q", got.SessionID, id)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("restored %d message(s); want 1", len(got.Messages))
	}
}

// A fresh session (no id) restores the caller's prompt untouched.
func TestRestoreSession_FreshSessionKeepsCallerPrompt(t *testing.T) {
	got, err := restoreSession(nil, "", "BASE PROMPT")
	if err != nil {
		t.Fatalf("restoreSession() error: %v", err)
	}
	if got.SystemPrompt != "BASE PROMPT" {
		t.Fatalf("SystemPrompt = %q; want the caller's prompt", got.SystemPrompt)
	}
}

func TestRestoreSession_CarriesPersistedUsage(t *testing.T) {
	db := newResumeFixture(t, "resume-me")
	if err := db.UpdateSessionUsage("resume-me", 700, 300, 120, 50, 20); err != nil {
		t.Fatalf("update session usage: %v", err)
	}

	restored, err := restoreSession(db, "resume-me", "base")
	if err != nil {
		t.Fatalf("restoreSession: %v", err)
	}

	want := restoredUsage{TokensIn: 700, TokensOut: 300, TokensCached: 120, SubTokensIn: 50, SubTokensOut: 20}
	if restored.Usage != want {
		t.Errorf("Usage = %+v, want %+v", restored.Usage, want)
	}
}

func TestRestoreSession_FreshSessionHasNoUsage(t *testing.T) {
	restored, err := restoreSession(nil, "", "base")
	if err != nil {
		t.Fatalf("restoreSession: %v", err)
	}
	if restored.Usage != (restoredUsage{}) {
		t.Errorf("Usage = %+v, want zero", restored.Usage)
	}
}

func TestSeedRestoredUsage_RestoresCountersAndFlushBaselines(t *testing.T) {
	s := &agentSession{}
	u := restoredUsage{TokensIn: 700, TokensOut: 300, TokensCached: 120, SubTokensIn: 50, SubTokensOut: 20}

	s.seedRestoredUsage(u)

	if s.totalUsage.PromptTokens != 700 || s.totalUsage.CompletionTokens != 300 ||
		s.totalUsage.CachedPromptTokens != 120 || s.totalUsage.TotalTokens != 1000 {
		t.Errorf("totalUsage = %+v, want prompt 700 completion 300 cached 120 total 1000", s.totalUsage)
	}
	if s.subUsage.PromptTokens != 50 || s.subUsage.CompletionTokens != 20 || s.subUsage.TotalTokens != 70 {
		t.Errorf("subUsage = %+v, want prompt 50 completion 20 total 70", s.subUsage)
	}
	if s.flushIn != 700 || s.flushOut != 300 || s.flushCached != 120 ||
		s.flushSubIn != 50 || s.flushSubOut != 20 {
		t.Errorf("flush baselines = %d/%d/%d/%d/%d, want 700/300/120/50/20",
			s.flushIn, s.flushOut, s.flushCached, s.flushSubIn, s.flushSubOut)
	}
}

func TestFlushUsageValues_AfterResumeDoesNotShrinkRow(t *testing.T) {
	db := newResumeFixture(t, "resume-me")
	if err := db.UpdateSessionUsage("resume-me", 700, 300, 120, 50, 20); err != nil {
		t.Fatalf("update session usage: %v", err)
	}
	session, err := db.GetSession("resume-me")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}

	s := &agentSession{db: db, sessionID: "resume-me"}
	s.seedRestoredUsage(restoredUsage{
		TokensIn: session.TokensIn, TokensOut: session.TokensOut,
		TokensCached: session.TokensCached, SubTokensIn: session.SubTokensIn,
		SubTokensOut: session.SubTokensOut,
	})

	// A flush below the restored totals must not shrink the persisted row:
	// without seeded flush baselines the guard would let this through and
	// overwrite the larger already-flushed totals.
	s.flushUsageValues(600, 250, 100, 40, 10)
	after, err := db.GetSession("resume-me")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if after.TokensIn != 700 || after.TokensOut != 300 || after.TokensCached != 120 ||
		after.SubTokensIn != 50 || after.SubTokensOut != 20 {
		t.Errorf("row shrank after resume: got in %d out %d cached %d sub %d/%d, want 700/300/120/50/20",
			after.TokensIn, after.TokensOut, after.TokensCached, after.SubTokensIn, after.SubTokensOut)
	}

	// A real increase continues from the restored totals.
	s.flushUsageValues(750, 320, 130, 60, 25)
	after, err = db.GetSession("resume-me")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if after.TokensIn != 750 || after.TokensOut != 320 || after.TokensCached != 130 ||
		after.SubTokensIn != 60 || after.SubTokensOut != 25 {
		t.Errorf("row after increase = in %d out %d cached %d sub %d/%d, want 750/320/130/60/25",
			after.TokensIn, after.TokensOut, after.TokensCached, after.SubTokensIn, after.SubTokensOut)
	}
}

// TestSetCtrlCh_PublishesRestoredUsage pins the startup publish: a resumed
// session seeds its usage but, without a run having completed, nothing
// emits a UsageInfo — so the info pane showed zero usage until the first
// run. SetCtrlCh publishes the current split as soon as a control channel
// attaches.
func TestSetCtrlCh_PublishesRestoredUsage(t *testing.T) {
	db := newResumeFixture(t, "resume-me")
	if err := db.UpdateSessionUsage("resume-me", 700, 300, 120, 50, 20); err != nil {
		t.Fatalf("update session usage: %v", err)
	}

	s := &agentSession{db: db, sessionID: "resume-me", toolReg: tools.NewRegistry()}
	s.seedRestoredUsage(restoredUsage{TokensIn: 700, TokensOut: 300, TokensCached: 120, SubTokensIn: 50, SubTokensOut: 20})

	ch := make(chan control.Msg, 8)
	s.SetCtrlCh(ch)

	select {
	case m := <-ch:
		got, ok := m.(*control.UsageInfo)
		if !ok {
			t.Fatalf("first control message = %T, want *control.UsageInfo", m)
		}
		// Orchestrator split is total minus sub-agent spend.
		if got.Agent.PromptTokens != 650 || got.Agent.CompletionTokens != 280 ||
			got.Agent.CachedTokens != 120 || got.Agent.TotalTokens != 930 {
			t.Errorf("agent usage = %+v, want prompt 650 completion 280 cached 120 total 930", got.Agent)
		}
		if got.Sub.PromptTokens != 50 || got.Sub.CompletionTokens != 20 || got.Sub.TotalTokens != 70 {
			t.Errorf("sub usage = %+v, want prompt 50 completion 20 total 70", got.Sub)
		}
	default:
		t.Fatal("SetCtrlCh did not publish usage to the control channel")
	}
}

// TestStoredSessionModel pins the resume-side model-pick restore (parity
// W4): the "provider/model" wire form reapplies only when it names a
// provider this config still defines; a legacy bare model name, an empty
// column, and a vanished provider all fall back to the routing chain with
// a warning instead of crossing to a wrong endpoint.
func TestStoredSessionModel(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{"zai": {}}}

	t.Run("wire form reapplies", func(t *testing.T) {
		db := newResumeFixture(t, "sess-m1")
		if err := db.SetSessionModel("sess-m1", "zai", "GLM-5.3"); err != nil {
			t.Fatalf("SetSessionModel: %v", err)
		}
		provider, model, ok := storedSessionModel(db, "sess-m1", cfg)
		if !ok || provider != "zai" || model != "GLM-5.3" {
			t.Fatalf("got %q/%q ok=%v; want zai/GLM-5.3 ok=true", provider, model, ok)
		}
	})

	t.Run("legacy bare model name is ignored", func(t *testing.T) {
		db := newResumeFixtureFor(t, "sess-m2", "GLM-5.3")
		// Pre-W4 rows carry the bare model name from the CreateSession
		// before W4 switched the column to the wire form.
		if _, _, ok := storedSessionModel(db, "sess-m2", cfg); ok {
			t.Fatal("a bare model name must not reapplies as a pick")
		}
	})

	t.Run("empty column is no pick", func(t *testing.T) {
		db := newResumeFixture(t, "sess-m3")
		if _, _, ok := storedSessionModel(db, "sess-m3", cfg); ok {
			t.Fatal("an empty stored model reads as no pick")
		}
	})

	t.Run("vanished provider falls back", func(t *testing.T) {
		db := newResumeFixture(t, "sess-m4")
		if err := db.SetSessionModel("sess-m4", "gone", "m"); err != nil {
			t.Fatalf("SetSessionModel: %v", err)
		}
		if _, _, ok := storedSessionModel(db, "sess-m4", cfg); ok {
			t.Fatal("a provider the config no longer defines must not reapplies")
		}
	})

	t.Run("no store or no session is no pick", func(t *testing.T) {
		if _, _, ok := storedSessionModel(nil, "sess-x", cfg); ok {
			t.Fatal("nil store must be no pick")
		}
		db := newResumeFixture(t, "sess-have")
		if _, _, ok := storedSessionModel(db, "sess-absent", cfg); ok {
			t.Fatal("an unknown session id must be no pick")
		}
	})
}

// TestWireModelValue pins the column's wire-rendering rule: unset model is
// "", an unset provider keeps the bare model name (pre-W4 shape), both set
// split.
func TestWireModelValue(t *testing.T) {
	for _, tc := range []struct{ provider, model, want string }{
		{"zai", "GLM-5.3", "zai/GLM-5.3"},
		{"", "GLM-5.3", "GLM-5.3"},
		{"zai", "", ""},
		{"", "", ""},
	} {
		if got := wireModelValue(tc.provider, tc.model); got != tc.want {
			t.Errorf("wireModelValue(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

// newResumeFixtureFor is newResumeFixture with a Session.Model value, so a
// test can seed the legacy bare-name shape the pre-W4 CreateSession wrote.
func newResumeFixtureFor(t *testing.T, id, model string) *memory.DB {
	t.Helper()
	db, err := memory.Open(filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatalf("memory.Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().Unix()
	if err := db.CreateSession(memory.Session{ID: id, StartedAt: now, Model: model}); err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := db.AddMessage(memory.Message{
		ID: "msg-1", SessionID: id, Idx: 0, Role: "user", Content: "plan this work", Timestamp: now,
	}); err != nil {
		t.Fatalf("AddMessage() error: %v", err)
	}
	return db
}

// TestSetModelPersistsPick pins the switch-side half of parity W4: a
// mid-session model switch writes the row the resume restores, so a
// crash or a kill after the switch still comes back routed where the
// session was switched to.
func TestSetModelPersistsPick(t *testing.T) {
	const id = "sess-persist"
	db := newResumeFixture(t, id)

	cfg := &config.Config{Providers: map[string]config.Provider{"zai": {}}}
	s := &agentSession{cfg: cfg, db: db, sessionID: id}

	s.SetModel("zai", "GLM-5.3")

	got, err := db.SessionModel(id)
	if err != nil || got != "zai/GLM-5.3" {
		t.Fatalf("stored pick = %q, %v; want zai/GLM-5.3", got, err)
	}
	provider, model := s.Model()
	if provider != "zai" || model != "GLM-5.3" {
		t.Fatalf("in-memory pick = %q/%q; want zai/GLM-5.3", provider, model)
	}
}

// TestSetPersonaReplacesPersistedPick pins the review blocker on PR #851:
// a persona switch supersedes the in-process pick (applyPersonaRouting)
// AND the persisted one — a resume re-derives persona routing AND applies
// the stored pick, so a stale pre-switch pick in the row would silently
// override the fresh route.
func TestSetPersonaReplacesPersistedPick(t *testing.T) {
	const id = "sess-route"
	db := newResumeFixture(t, id)

	// Two usable providers, so both the pick and the persona route are
	// constructible at SetModel/SetPersona time.
	cfg := &config.Config{Providers: map[string]config.Provider{
		"zai":   {BaseURL: "http://zai.test", APIKey: "k-zai"},
		"zanth": {BaseURL: "http://zanth.test", APIKey: "k-zanth"},
	}}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "routed.md"),
		[]byte("---\nname: Routed\ndescription: routes itself\ntools: []\nprovider: zanth\nmodel: claude-x\n---\nROUTED BODY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(nil) })

	s := &agentSession{cfg: cfg, db: db, sessionID: id}
	s.SetModel("zai", "GLM-5.3")
	if got, _ := db.SessionModel(id); got != "zai/GLM-5.3" {
		t.Fatalf("precondition: stored pick = %q", got)
	}

	if err := s.SetPersona("routed"); err != nil {
		t.Fatalf("SetPersona(routed): %v", err)
	}

	// The row must now carry the ROUTE (what the session last ran with),
	// not the pre-switch pick.
	if got, _ := db.SessionModel(id); got != "zanth/claude-x" {
		t.Fatalf("stored pick after persona switch = %q; want zanth/claude-x (the stale zai pick must not survive the switch)", got)
	}
	if provider, model := s.Model(); provider != "zanth" || model != "claude-x" {
		t.Fatalf("in-process routing = %q/%q; want zanth/claude-x", provider, model)
	}
}

// TestApplyStoredModelPick pins the resume pick's provider posture (review
// finding 2): an unusable pick provider — defined in config but missing
// key/URL, or dropped — keeps the persona route's names AND instance, the
// same warn-and-keep personaRouteFor applies, instead of leaving a nil
// main provider under switched names.
func TestApplyStoredModelPick(t *testing.T) {
	zai := config.Provider{BaseURL: "http://zai.test", APIKey: "k-zai"}
	keyless := config.Provider{} // no key, no URL: makeProvider must refuse it
	cfgWith := func(extra map[string]config.Provider) *config.Config {
		providers := map[string]config.Provider{"zai": zai}
		for k, v := range extra {
			providers[k] = v
		}
		return &config.Config{Providers: providers}
	}
	pickFixture := func(t *testing.T, id, stored string) *memory.DB {
		return newResumeFixtureFor(t, id, stored)
	}

	t.Run("same provider pins the model on the route's instance", func(t *testing.T) {
		cfg := cfgWith(nil)
		routeProv, _ := makeProvider("zai", zai)
		lc := &sessionConfig{cfg: cfg, provider: routeProv, providerName: "zai", modelName: "GLM-5"}
		db := pickFixture(t, "sess-p1", "zai/other")
		applyStoredModelPick(lc, personaRoute{providerName: "zai", modelName: "GLM-5"}, db, "sess-p1")
		if lc.modelName != "other" {
			t.Errorf("model = %q, want the pick", lc.modelName)
		}
		if lc.providerName != "zai" {
			t.Errorf("provider name moved: %q", lc.providerName)
		}
		if lc.provider != routeProv {
			t.Error("the route's provider instance must stay")
		}
	})

	t.Run("usable provider swap takes the instance", func(t *testing.T) {
		zanth := config.Provider{BaseURL: "http://zanth.test", APIKey: "k-zanth"}
		cfg := cfgWith(map[string]config.Provider{"zanth": zanth})
		routeProv, _ := makeProvider("zai", zai)
		lc := &sessionConfig{cfg: cfg, provider: routeProv, providerName: "zai", modelName: "GLM-5"}
		db := pickFixture(t, "sess-p2", "zanth/claude-x")
		applyStoredModelPick(lc, personaRoute{providerName: "zai", modelName: "GLM-5"}, db, "sess-p2")
		if lc.providerName != "zanth" || lc.modelName != "claude-x" {
			t.Fatalf("names = %q/%q; want zanth/claude-x", lc.providerName, lc.modelName)
		}
		// Instance identity across two makeProvider calls is not assumed
		// and not guaranteed; the invariant is behavioral: the pick built
		// a LIVE instance and it is not the route's stale one.
		if lc.provider == nil || lc.provider == routeProv {
			t.Error("the pick's live provider instance must replace the route's")
		}
	})

	t.Run("unusable provider keeps the route", func(t *testing.T) {
		// A provider entry that cannot construct (no key, no OAuth) is the
		// review finding's delayed-panic shape: the route must survive.
		cfg := cfgWith(map[string]config.Provider{"keyless": keyless})
		routeProv, _ := makeProvider("zai", zai)
		lc := &sessionConfig{cfg: cfg, provider: routeProv, providerName: "zai", modelName: "GLM-5"}
		db := pickFixture(t, "sess-p3", "keyless/mystery")
		applyStoredModelPick(lc, personaRoute{providerName: "zai", modelName: "GLM-5"}, db, "sess-p3")
		if lc.providerName != "zai" || lc.modelName != "GLM-5" {
			t.Fatalf("names = %q/%q; the route must survive an unusable pick provider", lc.providerName, lc.modelName)
		}
		if lc.provider != routeProv {
			t.Error("the route's provider instance must survive — never a nil main provider under switched names")
		}
	})

	t.Run("dropped provider keeps the route", func(t *testing.T) {
		cfg := cfgWith(nil)
		routeProv, _ := makeProvider("zai", zai)
		lc := &sessionConfig{cfg: cfg, provider: routeProv, providerName: "zai", modelName: "GLM-5"}
		db := pickFixture(t, "sess-p4", "gone/m")
		applyStoredModelPick(lc, personaRoute{providerName: "zai", modelName: "GLM-5"}, db, "sess-p4")
		if lc.providerName != "zai" || lc.modelName != "GLM-5" {
			t.Fatalf("names = %q/%q; a dropped pick provider must keep the route", lc.providerName, lc.modelName)
		}
	})
}
