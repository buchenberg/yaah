package doctor

import (
	"errors"
	"testing"

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
		{"absent/off", false, "", "OK"},
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

	cfg.Agent.SubAgent.Containment = ""
	if c := CheckContainment(cfg, nil); c.Status != "OK" {
		t.Errorf("default containment: status = %q, want OK (%s)", c.Status, c.Detail)
	}
	if c := CheckContainment(nil, errors.New("boom")); c.Status != "WARN" {
		t.Errorf("config error: status = %q, want WARN", c.Status)
	}
}
