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
type stubBackend struct {
	usable     bool
	netCapable bool
}

func (stubBackend) Name() string                               { return "stub" }
func (stubBackend) EnforcementTier() string                    { return "stub-tier" }
func (b stubBackend) Available() (bool, string)                { return b.usable, "" }
func (b stubBackend) SupportsNetwork(containment.NetMode) bool { return b.netCapable }
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
		{"auto, usable backend", confinement.PlacementAuto, true, stubBackend{usable: true, netCapable: true}, "paddock", true, true},
		{"paddock, usable backend", confinement.PlacementPaddock, true, stubBackend{usable: true, netCapable: true}, "paddock", true, true},
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
		got, err := SubAgentNetworkPolicy(tc.mode, tc.broker)
		if tc.wantErr {
			if err == nil {
				t.Errorf("SubAgentNetworkPolicy(%q, %d): want an error, got %+v", tc.mode, tc.broker, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("SubAgentNetworkPolicy(%q, %d): %v", tc.mode, tc.broker, err)
			continue
		}
		if got.Mode != tc.wantMode || got.BrokerPort != tc.wantPort {
			t.Errorf("SubAgentNetworkPolicy(%q, %d) = %+v, want mode %q port %d", tc.mode, tc.broker, got, tc.wantMode, tc.wantPort)
		}
	}
}

// An unset policy means "no posture configured", which is the config default —
// allow_all — and resolveSubAgentConfinement normalizes it so the recorded
// posture is the one actually applied. Handing an empty mode through would mean
// deny_all (the runtime's own default), i.e. a jail that refuses every connect
// because nobody set a key.
func TestResolveSubAgentConfinement_UnsetNetworkIsTheConfigDefault(t *testing.T) {
	if b := landlock.Detect(); b == nil {
		t.Skip("no Landlock on this host")
	} else if ok, _ := b.Available(); !ok {
		t.Skip("Landlock present but unusable on this host")
	}
	pv := tools.NewPathValidator(t.TempDir(), false, nil)
	_, rec, err := resolveSubAgentConfinement(pv, config.SubAgentConfig{Containment: "auto"}, t.TempDir(), containment.NetworkPolicy{})
	if err != nil {
		t.Fatalf("an unset network must not refuse the dispatch: %v", err)
	}
	if rec.Network != string(containment.NetAllowAll) {
		t.Errorf("an unset network records %q, want allow_all", rec.Network)
	}
}

// A restrictive egress posture cannot ride the advisory fallback, and cannot be
// claimed on a host whose jail has no network rules: in both cases the dispatch
// refuses rather than running — or recording — open egress.
func TestRequireEnforceableEgress(t *testing.T) {
	deny := containment.NetworkPolicy{Mode: containment.NetDenyAll}
	allow := containment.NetworkPolicy{Mode: containment.NetAllowAll}
	broker := containment.NetworkPolicy{Mode: containment.NetBroker, BrokerPort: 41234}
	usableNoNet := stubBackend{usable: true, netCapable: false}
	usableNet := stubBackend{usable: true, netCapable: true}

	const workdir = "/work/wt"
	cases := []struct {
		name      string
		available bool
		backend   containment.Backend
		network   containment.NetworkPolicy
		placement confinement.Placement
		workdir   string
		wantErr   bool
		wantHint  string
	}{
		{"allow_all needs nothing", false, nil, allow, confinement.PlacementAuto, workdir, false, ""},
		{"allow_all with no placement needs nothing", false, nil, allow, "", "", false, ""},
		{"deny_all with no backend refuses", false, nil, deny, confinement.PlacementAuto, workdir, true, "no usable syscall-jail backend"},
		{"broker with no backend refuses", false, nil, broker, confinement.PlacementAuto, workdir, true, "no usable syscall-jail backend"},
		{"deny_all without network rules refuses", true, usableNoNet, deny, confinement.PlacementAuto, workdir, true, "ABI >= 4"},
		{"broker without network rules refuses", true, usableNoNet, broker, confinement.PlacementAuto, workdir, true, "ABI >= 4"},
		{"deny_all with rules is fine", true, usableNet, deny, confinement.PlacementAuto, workdir, false, ""},
		{"broker with rules is fine", true, usableNet, broker, confinement.PlacementPaddock, workdir, false, ""},
		// paddock already fails closed at Exec with its own message.
		{"paddock with no backend keeps its own refusal", false, nil, deny, confinement.PlacementPaddock, workdir, false, ""},
		// The fail-open combinations the review caught: a posture that would be
		// dropped because the dispatch is not jailed at all.
		{"containment off + deny_all refuses", true, usableNet, deny, "", workdir, true, "containment is"},
		{"containment off + broker refuses", true, usableNet, broker, "", workdir, true, "containment is"},
		{"no workdir + deny_all refuses", true, usableNet, deny, confinement.PlacementAuto, "", true, "no workdir"},
	}
	for _, c := range cases {
		err := requireEnforceableEgress(c.available, c.backend, c.network, c.placement, c.workdir, "auto")
		if c.wantErr && err == nil {
			t.Errorf("%s: want a refusal, got none", c.name)
			continue
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: unexpected refusal: %v", c.name, err)
			continue
		}
		if c.wantHint != "" && err != nil && !strings.Contains(err.Error(), c.wantHint) {
			t.Errorf("%s: error %q should mention %q", c.name, err, c.wantHint)
		}
	}
}

// The broker is advertised the way HTTP proxies are advertised: a pin without
// routing would permit only the broker's port while every ordinary fetch still
// went direct and was refused.
func TestBrokerProxyEnv(t *testing.T) {
	if got := brokerProxyEnv(containment.NetworkPolicy{Mode: containment.NetAllowAll}); got != nil {
		t.Errorf("allow_all must set no proxy, got %v", got)
	}
	env := brokerProxyEnv(containment.NetworkPolicy{Mode: containment.NetBroker, BrokerPort: 41234})
	joined := strings.Join(env, " ")
	for _, want := range []string{"HTTPS_PROXY=http://127.0.0.1:41234", "http_proxy=http://127.0.0.1:41234", "NO_PROXY="} {
		if !strings.Contains(joined, want) {
			t.Errorf("broker proxy env must contain %q, got %v", want, env)
		}
	}
}

// A restrictive egress posture omits the tools that would make their request from
// THIS process, because the jail governs the dispatch's own processes and not
// yaah's — otherwise deny_all would have a one-call hole in it.
func TestBuildSubAgentRegistry_RestrictiveEgressOmitsHostNetworkTools(t *testing.T) {
	pv := tools.NewPathValidator(t.TempDir(), false, nil)
	profile := subagent.RoleProfile{Tools: []string{"read", "webfetch", "http", "grep"}}

	// Default posture: the tools stay, as they always have.
	open, _, err := buildSubAgentRegistry(
		taskRunnerOpts{pathValidator: pv, subCfg: config.SubAgentConfig{}, subNetwork: allowAllNetwork()},
		profile, 0, t.TempDir())
	if err != nil {
		t.Fatalf("allow_all: %v", err)
	}
	for _, name := range []string{"http", "webfetch"} {
		if open.Get(name) == nil {
			t.Errorf("allow_all must keep %s", name)
		}
	}

	// A restrictive posture on a host that can jail: they are gone.
	if b := landlock.Detect(); b == nil {
		t.Skip("no Landlock on this host")
	} else if ok, _ := b.Available(); !ok {
		t.Skip("Landlock present but unusable on this host")
	} else if !b.SupportsNetwork(containment.NetDenyAll) {
		t.Skip("no Landlock network support on this host")
	}
	closed, _, err := buildSubAgentRegistry(
		taskRunnerOpts{
			pathValidator: pv,
			subCfg:        config.SubAgentConfig{},
			subNetwork:    containment.NetworkPolicy{Mode: containment.NetDenyAll},
		},
		profile, 0, t.TempDir())
	if err != nil {
		t.Fatalf("deny_all: %v", err)
	}
	for _, name := range []string{"http", "webfetch"} {
		if closed.Get(name) != nil {
			t.Errorf("a restrictive dispatch must omit %s (it would run outside the jail)", name)
		}
	}
	if closed.Get("read") == nil {
		t.Error("only the host-network tools are omitted")
	}
}

// The prompt must name the omission, so a missing tool does not read as a broken
// profile.
func TestBuildSubAgentSysPrompt_NamesOmittedHostNetworkTools(t *testing.T) {
	opts := taskRunnerOpts{}
	profile := subagent.RoleProfile{Tools: []string{"read", "webfetch", "http"}}
	got := buildSubAgentSysPrompt(opts, "developer", profile, false, "/wt",
		&subAgentConfinement{Requested: "auto", Resolved: "paddock", Enforced: true, Network: "deny_all"})
	if !strings.Contains(got, "webfetch") || !strings.Contains(got, "http") {
		t.Errorf("the omission must be named:\\n%s", got)
	}
}
