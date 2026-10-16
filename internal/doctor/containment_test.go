package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"

	"github.com/buchenberg/yaah/internal/config"
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
		got := containmentCheck(c.available, "test-backend", "retained", c.placement)
		if got.Status != c.want {
			t.Errorf("%s: status = %q, want %q (%s)", c.name, got.Status, c.want, got.Detail)
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
	if landlock.Detect() != nil {
		want = "OK"
	}
	if c.Status != want {
		t.Errorf("default containment: status = %q, want %q (%s)", c.Status, want, c.Detail)
	}
	if c := CheckContainment(nil, errors.New("boom")); c.Status != "WARN" {
		t.Errorf("config error: status = %q, want WARN", c.Status)
	}
}
