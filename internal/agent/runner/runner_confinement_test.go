package runner

import (
	"testing"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/confinement"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/containment"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

// stubBackend stands in for a syscall-jail backend so both sides of the
// availability decision are testable; `nil` stands in for "none on this host".
// A live backend cannot produce "present but unusable".
type stubBackend struct{ usable bool }

func (stubBackend) Name() string                             { return "stub" }
func (stubBackend) EnforcementTier() string                  { return "stub-tier" }
func (b stubBackend) Available() (bool, string)              { return b.usable, "" }
func (stubBackend) SupportsNetwork(containment.NetMode) bool { return true }
func (stubBackend) ProfileFor([]string, containment.NetworkPolicy) string {
	return ""
}
func (stubBackend) Probe(string, string, []string) error { return nil }
func (stubBackend) Launch(string, containment.LaunchRequest) (containment.LaunchResult, error) {
	return containment.LaunchResult{}, nil
}

// The recorded placement must describe what the dispatch will actually do — the
// bug Kilo caught was recording "paddock" for an unusable backend, which Exec
// would silently run advisory (or refuse), so an advisory run read as jailed.
func TestConfinementRecord_Placement(t *testing.T) {
	cases := []struct {
		name         string
		placement    confinement.Placement
		available    bool
		backend      containment.Backend
		wantResolved string
		wantEnforced bool
		wantConfine  bool // build a workspace (so Exec can jail or fail closed)
	}{
		{"auto, no backend", confinement.PlacementAuto, false, nil, "advisory", false, false},
		{"auto, unusable backend", confinement.PlacementAuto, false, stubBackend{}, "advisory", false, false},
		{"paddock, no backend", confinement.PlacementPaddock, false, nil, "paddock", false, true},
		{"paddock, unusable backend", confinement.PlacementPaddock, false, stubBackend{}, "paddock", false, true},
		{"auto, usable backend", confinement.PlacementAuto, true, stubBackend{usable: true}, "paddock", true, true},
		{"paddock, usable backend", confinement.PlacementPaddock, true, stubBackend{usable: true}, "paddock", true, true},
	}
	for _, c := range cases {
		rec, confine := confinementRecord("auto", c.placement, c.available, c.backend, "/work")
		if rec.Resolved != c.wantResolved {
			t.Errorf("%s: resolved = %q, want %q", c.name, rec.Resolved, c.wantResolved)
		}
		if rec.Enforced != c.wantEnforced {
			t.Errorf("%s: enforced = %v, want %v", c.name, rec.Enforced, c.wantEnforced)
		}
		if confine != c.wantConfine {
			t.Errorf("%s: confine = %v, want %v", c.name, confine, c.wantConfine)
		}
		if !rec.Enforced && (rec.Backend != "" || rec.Tier != "") {
			t.Errorf("%s: an unenforced record must not name a backend/tier, got %q/%q", c.name, rec.Backend, rec.Tier)
		}
		if c.wantEnforced && (rec.Backend == "" || rec.Tier == "") {
			t.Errorf("%s: an enforced record must name the backend and tier", c.name)
		}
		// Only a jail claims a writable root; advisory claims nothing.
		if (rec.Writable != "") != (rec.Resolved == "paddock") {
			t.Errorf("%s: writable = %q with resolved %q", c.name, rec.Writable, rec.Resolved)
		}
	}
}

// Plan 7 Phase 3: the workdir lane attaches a confiner when configured, and
// buildSubAgentRegistry hands back the resolution so the dispatch can record it —
// including the advisory case, which returns no workspace but DOES have a record
// (the very resolution the trace must show). Since the flip, an unset setting is
// the auto default, so the "just works" path records too.
func TestBuildSubAgentRegistry_ReturnsConfinementRecord(t *testing.T) {
	pv := tools.NewPathValidator(t.TempDir(), false, nil)
	profile := subagent.RoleProfile{Tools: []string{"read", "grep"}}

	if _, conf, err := buildSubAgentRegistry(
		taskRunnerOpts{pathValidator: pv, subCfg: config.SubAgentConfig{Containment: "off"}},
		profile, 0, t.TempDir()); err != nil {
		t.Fatalf("off: %v", err)
	} else if conf != nil {
		t.Errorf("off must not record a resolution, got %+v", conf)
	}

	// An unset setting (the zero value — what an untouched config produces) is
	// the auto default, and must record.
	if _, conf, err := buildSubAgentRegistry(
		taskRunnerOpts{pathValidator: pv, subCfg: config.SubAgentConfig{}},
		profile, 0, t.TempDir()); err != nil {
		t.Fatalf("default: %v", err)
	} else if conf == nil {
		t.Error("the default (auto) must record a resolution")
	}

	_, conf, err := buildSubAgentRegistry(
		taskRunnerOpts{pathValidator: pv, subCfg: config.SubAgentConfig{Containment: "auto"}},
		profile, 0, t.TempDir())
	if err != nil {
		t.Fatalf("auto: %v", err)
	}
	if conf == nil {
		t.Fatal("auto must record a resolution (advisory included)")
	}
	wantResolved := "advisory"
	if landlock.Detect() != nil {
		wantResolved = "paddock"
	}
	if conf.Resolved != wantResolved {
		t.Errorf("auto: resolved = %q, want %q", conf.Resolved, wantResolved)
	}
	if conf.Resolved == "advisory" && conf.Writable != "" {
		t.Errorf("an advisory resolution must not claim a writable root, got %q", conf.Writable)
	}
}

// The workdir lane attaches a confiner only when agents.subagent.containment
// asks for one, and records the resolution either way (Plan 7 Phase 3). "off"
// (or "none"/"advisory") leaves the host path — the guardrail that the
// orchestrator's lane and an explicitly-opted-out sub-agent stay unconfined.
func TestResolveSubAgentConfinement_Placement(t *testing.T) {
	pv := tools.NewPathValidator(t.TempDir(), false, nil)

	cases := []struct {
		setting string
		confine bool // a confined workspace is returned
		record  bool // a resolution record is returned
	}{
		{"off", false, false},
		{"none", false, false},
		{"advisory", false, false},
		{"paddock", true, true}, // fails closed even with no backend
		{"jail", true, true},
	}
	for _, c := range cases {
		ws, rec := resolveSubAgentConfinement(pv, config.SubAgentConfig{Containment: c.setting}, t.TempDir())
		if (ws != nil) != c.confine {
			t.Errorf("containment=%q: confined=%v, want %v", c.setting, ws != nil, c.confine)
		}
		if (rec != nil) != c.record {
			t.Errorf("containment=%q: record=%v, want %v", c.setting, rec != nil, c.record)
		}
		if rec != nil && rec.Resolved == "" {
			t.Errorf("containment=%q: empty resolved placement", c.setting)
		}
	}

	// "auto" jails only where a backend exists; elsewhere it degrades to
	// advisory — and either way records the resolution, so a reader can tell the
	// two apart. An unset setting is the same default, and must resolve
	// identically.
	autoWS, autoRec := resolveSubAgentConfinement(pv, config.SubAgentConfig{Containment: "auto"}, t.TempDir())
	wantJail := landlock.Detect() != nil
	if (autoWS != nil) != wantJail {
		t.Errorf("auto: confined=%v, want %v", autoWS != nil, wantJail)
	}
	if autoRec == nil {
		t.Fatal("auto must record a resolution")
	}
	wantResolved := "advisory"
	if wantJail {
		wantResolved = "paddock"
	}
	if autoRec.Resolved != wantResolved {
		t.Errorf("auto: resolved = %q, want %q", autoRec.Resolved, wantResolved)
	}
	if wantJail && (autoRec.Backend == "" || autoRec.Tier == "") {
		t.Errorf("auto (jailed): backend/tier must be recorded, got %q / %q", autoRec.Backend, autoRec.Tier)
	}

	defWS, defRec := resolveSubAgentConfinement(pv, config.SubAgentConfig{}, t.TempDir())
	if (defWS != nil) != wantJail {
		t.Errorf("the default must resolve like auto: confined=%v, want %v", defWS != nil, wantJail)
	}
	if defRec == nil || defRec.Resolved != wantResolved {
		t.Errorf("the default must resolve like auto: rec=%+v, want resolved %q", defRec, wantResolved)
	}
}
