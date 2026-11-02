package yaah

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/memory"
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

// The mode-change notice is the in-band cue that a switch happened, so it must
// name the new mode and the regime that mode enforces, and be deterministic
// enough for a view to render verbatim.
func TestModeChangeNotice(t *testing.T) {
	cases := []struct {
		name string
		mode string
		want []string
		deny []string
	}{
		{
			name: "plan names the mode and the withheld regime",
			mode: acpModePlan,
			want: []string{"Mode changed to Plan", "withheld", "inspection"},
			deny: []string{"do not mutate", "Build"},
		},
		{
			name: "build names the mode and the policy-only regime",
			mode: acpModeBuild,
			want: []string{"Mode changed to Build", "available", "do not mutate"},
			deny: []string{"withheld", "Plan"},
		},
		{
			name: "empty mode is build",
			mode: "",
			want: []string{"Mode changed to Build", "do not mutate"},
			deny: []string{"withheld"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := (&agentSession{}).modeChangeNotice(tc.mode)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("(&agentSession{}).modeChangeNotice(%q) = %q; want %q", tc.mode, got, want)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(got, deny) {
					t.Errorf("(&agentSession{}).modeChangeNotice(%q) = %q; must not contain %q", tc.mode, got, deny)
				}
			}
			if again := (&agentSession{}).modeChangeNotice(tc.mode); again != got {
				t.Errorf("(&agentSession{}).modeChangeNotice(%q) is not deterministic: %q vs %q", tc.mode, got, again)
			}
		})
	}
}

// The notice must reuse the directive's regime wording rather than restate it:
// one copied sentence is what lets a notice promise a regime the loop does not
// enforce.
func TestModeChangeNotice_ReusesRegimeSentence(t *testing.T) {
	for _, mode := range []string{acpModePlan, acpModeBuild, ""} {
		level := agent.FullTools
		if mode == planModeSpec.Mode {
			level = planModeSpec.ToolsLevel
		}
		want := "Mode changed to " + modeLabel(mode) + " — " + regimeSentence(level)
		if got := (&agentSession{}).modeChangeNotice(mode); got != want {
			t.Errorf("(&agentSession{}).modeChangeNotice(%q) = %q; want %q", mode, got, want)
		}
	}
}

// SetMode announces the switch to the session's current view, so a mid-session
// switch is observable in-band in every frontend.
func TestSetModeDispatchesModeChangedEvent(t *testing.T) {
	sess := &agentSession{}
	view := &safeCaptureView{}
	sess.SetView(view)

	if err := sess.SetMode(acpModePlan); err != nil {
		t.Fatalf("SetMode(%q) = %v", acpModePlan, err)
	}
	if len(view.events) != 1 {
		t.Fatalf("view received %d event(s) on a mode switch, want 1", len(view.events))
	}
	evt, ok := view.events[0].(*agent.ModeChangedEvent)
	if !ok {
		t.Fatalf("dispatched event = %T; want *agent.ModeChangedEvent", view.events[0])
	}
	if evt.Mode != acpModePlan {
		t.Errorf("event Mode = %q; want %q", evt.Mode, acpModePlan)
	}
	if evt.Notice == "" {
		t.Fatal("event Notice is empty; the views render it verbatim")
	}
	if evt.Notice != (&agentSession{}).modeChangeNotice(acpModePlan) {
		t.Errorf("event Notice = %q; want %q", evt.Notice, (&agentSession{}).modeChangeNotice(acpModePlan))
	}
	for _, want := range []string{"Plan", "withheld"} {
		if !strings.Contains(evt.Notice, want) {
			t.Errorf("plan notice %q missing %q", evt.Notice, want)
		}
	}

	if err := sess.SetMode(acpModeBuild); err != nil {
		t.Fatalf("SetMode(%q) = %v", acpModeBuild, err)
	}
	last, ok := view.events[len(view.events)-1].(*agent.ModeChangedEvent)
	if !ok {
		t.Fatalf("last event = %T; want *agent.ModeChangedEvent", view.events[len(view.events)-1])
	}
	if last.Mode != acpModeBuild {
		t.Errorf("build event Mode = %q; want %q", last.Mode, acpModeBuild)
	}
	for _, want := range []string{"Build", "do not mutate"} {
		if !strings.Contains(last.Notice, want) {
			t.Errorf("build notice %q missing %q", last.Notice, want)
		}
	}
}

// A refused mode changes nothing and announces nothing: no notice may claim a
// switch that did not happen.
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
	if _, ok := view.events[0].(*agent.ModeChangedEvent); !ok {
		t.Fatalf("delivered event = %T; want *agent.ModeChangedEvent", view.events[0])
	}
}

// storedSessionMode reads the mode a session row carries, the same read resume
// performs.
func storedSessionMode(t *testing.T, db *memory.DB, id string) string {
	t.Helper()
	mode, err := db.SessionMode(id)
	if err != nil {
		t.Fatalf("SessionMode(%q) error: %v", id, err)
	}
	return mode
}

// A switch is persisted on the switch, so a later resume can restore it: the
// session row carries the selection, not only this process's memory. Without
// this, a plan-mode session silently widened to build on resume.
func TestSetModePersistsSessionMode(t *testing.T) {
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
	if got := storedSessionMode(t, db, "sess-mode"); got != acpModePlan {
		t.Fatalf("persisted mode = %q after switching to %q; want %q", got, acpModePlan, acpModePlan)
	}

	// A refused mode persists nothing and leaves the stored selection alone.
	if err := sess.SetMode("yolo"); err == nil {
		t.Fatalf("SetMode(%q) = nil; want an error", "yolo")
	}
	if got := storedSessionMode(t, db, "sess-mode"); got != acpModePlan {
		t.Fatalf("persisted mode = %q after a refused switch; want %q", got, acpModePlan)
	}
	if got := sess.Mode(); got != acpModePlan {
		t.Fatalf("Mode() = %q after a refused switch; want %q", got, acpModePlan)
	}

	if err := sess.SetMode(acpModeBuild); err != nil {
		t.Fatalf("SetMode(%q) = %v", acpModeBuild, err)
	}
	if got := storedSessionMode(t, db, "sess-mode"); got != acpModeBuild {
		t.Fatalf("persisted mode = %q after switching back; want %q", got, acpModeBuild)
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
