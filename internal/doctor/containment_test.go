package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/jail"
)

// Plan 7 Phase 3: the pure decision behind CheckContainment, including the case
// a live backend cannot produce — present but unusable — so a fail-closed
// `paddock` is a FAIL, not a surprise at dispatch.
func TestContainmentCheck(t *testing.T) {
	cases := []struct {
		name      string
		available bool
		placement string
		want      string
	}{
		// An empty setting is the config default, which since Plan 7 Phase 3 is
		// "auto": jail where the host can, degrade visibly where it cannot.
		{"available/default", true, "", "OK"},
		{"absent/default", false, "", "WARN"},
		{"absent/off", false, "off", "OK"},
		{"absent/off-explicit", false, "off", "OK"},
		{"absent/advisory", false, "advisory", "OK"},
		{"available/paddock", true, "paddock", "OK"},
		{"available/auto", true, "auto", "OK"},
		{"absent/paddock", false, "paddock", "FAIL"},
		{"absent/jail", false, "jail", "FAIL"},
		{"unavailable/paddock", false, "paddock", "FAIL"},
		{"absent/auto", false, "auto", "WARN"},
	}
	for _, c := range cases {
		got := containmentCheck(c.available, "test-backend", "retained", c.placement, "", 0, true, false)
		if got.Status != c.want {
			t.Errorf("%s: status = %q, want %q (%s)", c.name, got.Status, c.want, got.Detail)
		}
	}
}

// Egress rides the jail (Plan 7 Phase 3): with confinement a restrictive posture
// is a control, and without it the posture is only a setting — which doctor must
// not present as enforced.
func TestContainmentCheck_EgressPosture(t *testing.T) {
	cases := []struct {
		name        string
		available   bool
		containment string
		network     string
		hosts       int
		capable     bool
		container   bool
		want        string
	}{
		{"jailed + deny_all", true, "auto", "deny_all", 0, true, false, "OK"},
		{"jailed + broker", true, "paddock", "broker", 2, true, false, "OK"},
		{"jailed + allow_all", true, "auto", "allow_all", 0, true, false, "OK"},
		{"no jail + allow_all", false, "off", "allow_all", 0, true, false, "OK"},
		// Filesystem-only Landlock (ABI 1-3): the jail exists, the network rule
		// does not, and the dispatch refuses rather than running open.
		{"jailed but no net + deny_all", true, "auto", "deny_all", 0, false, false, "FAIL"},
		{"jailed but no net + broker", true, "paddock", "broker", 1, false, false, "FAIL"},
		// No jail at all is the same outcome: the posture cannot be applied, and
		// the dispatch refuses rather than dropping it.
		{"no jail + deny_all", false, "auto", "deny_all", 0, false, false, "FAIL"},
		{"jail off + deny_all", true, "off", "deny_all", 0, true, false, "FAIL"},
		{"jail off + broker", true, "advisory", "broker", 1, true, false, "FAIL"},
		// A per-dispatch container is an enforcement substrate of its own.
		{"container + no jail + deny_all", false, "auto", "deny_all", 0, false, true, "OK"},
		{"container + no net + broker", true, "auto", "broker", 2, false, true, "OK"},
	}
	for _, c := range cases {
		got := containmentCheck(c.available, "test-backend", "retained", c.containment, c.network, c.hosts, c.capable, c.container)
		if got.Status != c.want {
			t.Errorf("%s: status = %q, want %q (%s)", c.name, got.Status, c.want, got.Detail)
		}
	}
	// The detail must show the posture and, for a broker, its allowlist size.
	ok := containmentCheck(true, "test-backend", "retained", "auto", "broker", 3, true, false)
	for _, want := range []string{"egress broker", "3 allowed host(s)", "enforced by the jail"} {
		if !strings.Contains(ok.Detail, want) {
			t.Errorf("detail %q must mention %q", ok.Detail, want)
		}
	}
}

func TestCheckContainment(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.SubAgent.Mode = "retained"

	// An unset setting is the config default, which reports the placement it
	// really resolves to (auto since Plan 7 Phase 3) and the status that follows
	// from this host's capability.
	cfg.Agent.SubAgent.Containment = ""
	c := CheckContainment(cfg, nil)
	if !strings.Contains(c.Detail, "placement auto") {
		t.Errorf("the default must resolve to auto, got %q", c.Detail)
	}
	want := "WARN"
	if jail.Backend() != nil {
		want = "OK"
	}
	if c.Status != want {
		t.Errorf("default containment: status = %q, want %q (%s)", c.Status, want, c.Detail)
	}
	if c := CheckContainment(nil, errors.New("boom")); c.Status != "WARN" {
		t.Errorf("config error: status = %q, want WARN", c.Status)
	}
}
