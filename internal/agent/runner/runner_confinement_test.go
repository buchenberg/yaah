package runner

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/confinement"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/containment"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

// A scratch that cannot be prepared must FAIL the dispatch, not be silently
// downgraded: without the redirect the jailed body's HOME/TMPDIR writes land
// outside the writable root and are denied, and dropping the jail instead would
// silently lose containment. A planted symlink is the hostile case that
// motivates it (and is real: the redirect's own writes happen unconfined).
func TestResolveSubAgentConfinement_RefusalFailsTheDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	pv := tools.NewPathValidator(t.TempDir(), false, nil)
	workdir := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(workdir, ".yaah-scratch")); err != nil {
		t.Fatalf("plant the symlink: %v", err)
	}
	// paddock fails closed even with no backend, so this reaches the redirect.
	ws, rec, err := resolveSubAgentConfinement(pv, config.SubAgentConfig{Containment: "paddock"}, workdir, allowAllNetwork())
	if err == nil {
		t.Fatalf("an unredirectable scratch must fail the dispatch, got ws=%v rec=%+v", ws, rec)
	}
	if !strings.Contains(err.Error(), ".yaah-scratch") {
		t.Errorf("the error must name the offending path, got %v", err)
	}

	// And the registry build surfaces it rather than confining without the
	// redirect.
	if _, _, rerr := buildSubAgentRegistry(
		taskRunnerOpts{pathValidator: pv, subCfg: config.SubAgentConfig{Containment: "paddock"}},
		subagent.RoleProfile{Tools: []string{"read", "grep"}}, 0, workdir); rerr == nil {
		t.Fatal("buildSubAgentRegistry must fail on an unredirectable scratch")
	}
}

// allowAllNetwork is the default posture, so confinement tests exercise the jail
// rather than an egress rule.
func allowAllNetwork() containment.NetworkPolicy {
	return containment.NetworkPolicy{Mode: containment.NetAllowAll}
}

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
		rec, confine := confinementRecord("auto", c.placement, c.available, c.backend, "/work", allowAllNetwork())
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
// (the very resolution the trace must show).
func TestBuildSubAgentRegistry_ReturnsConfinementRecord(t *testing.T) {
	pv := tools.NewPathValidator(t.TempDir(), false, nil)
	profile := subagent.RoleProfile{Tools: []string{"read", "grep"}}

	// "off" records nothing: no jail was requested, so there is no placement to
	// report.
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
// asks for one, and records the resolution either way (Plan 7 Phase 3). The
// default (and "off"/"advisory") leaves the host path — the guardrail that the
// orchestrator's lane and an unconfigured sub-agent stay unconfined.
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
		ws, rec, rerr := resolveSubAgentConfinement(pv, config.SubAgentConfig{Containment: c.setting}, t.TempDir(), allowAllNetwork())
		if rerr != nil {
			t.Errorf("containment=%q: %v", c.setting, rerr)
			continue
		}
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
	// two apart.
	autoWS, autoRec, autoErr := resolveSubAgentConfinement(pv, config.SubAgentConfig{Containment: "auto"}, t.TempDir(), allowAllNetwork())
	if autoErr != nil {
		t.Fatalf("auto: %v", autoErr)
	}
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

	// An unset setting is the same default and must resolve identically.
	defWS, defRec, defErr := resolveSubAgentConfinement(pv, config.SubAgentConfig{}, t.TempDir(), allowAllNetwork())
	if defErr != nil {
		t.Fatalf("default: %v", defErr)
	}
	if (defWS != nil) != wantJail {
		t.Errorf("the default must resolve like auto: confined=%v, want %v", defWS != nil, wantJail)
	}
	if defRec == nil || defRec.Resolved != wantResolved {
		t.Errorf("the default must resolve like auto: rec=%+v, want resolved %q", defRec, wantResolved)
	}
}

// The egress posture is lowered from config, and the one mode that cannot be
// honoured without session state (a live broker) is refused rather than
// downgraded to open egress.
func TestNetworkPolicy_Lowering(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		broker   int
		wantMode containment.NetMode
		wantPort int
		wantErr  bool
	}{
		{"", 0, containment.NetAllowAll, 0, false},
		{"allow_all", 0, containment.NetAllowAll, 0, false},
		{"deny_all", 0, containment.NetDenyAll, 0, false},
		{"DENY_ALL", 0, containment.NetDenyAll, 0, false},
		{"broker", 41234, containment.NetBroker, 41234, false},
		{"broker", 0, "", 0, true}, // no broker to pin to
		{"borker", 0, "", 0, true}, // unknown mode
	} {
		got, err := networkPolicy(tc.mode, tc.broker)
		if tc.wantErr {
			if err == nil {
				t.Errorf("networkPolicy(%q, %d): want an error, got %+v", tc.mode, tc.broker, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("networkPolicy(%q, %d): %v", tc.mode, tc.broker, err)
			continue
		}
		if got.Mode != tc.wantMode || got.BrokerPort != tc.wantPort {
			t.Errorf("networkPolicy(%q, %d) = %+v, want mode %q port %d", tc.mode, tc.broker, got, tc.wantMode, tc.wantPort)
		}
	}
}

// A record must never claim open egress the spec did not ask for: the zero value
// reads as deny_all, matching the runtime's fail-closed default.
func TestNetworkMode_ZeroValueIsDenyAll(t *testing.T) {
	if got := networkMode(containment.NetworkPolicy{}); got != string(containment.NetDenyAll) {
		t.Errorf("zero policy names %q, want %q", got, containment.NetDenyAll)
	}
	if got := networkMode(allowAllNetwork()); got != string(containment.NetAllowAll) {
		t.Errorf("allow_all names %q", got)
	}
}
