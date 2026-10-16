package doctor

import (
	"errors"
	"testing"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
	"github.com/buchenberg/yaah/internal/config"
)

// Plan 7 Phase 3: doctor reports the sub-agent jail capability, and a
// fail-closed `paddock` placement with no backend is a FAIL rather than a
// surprise at dispatch time.
func TestCheckContainment(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.SubAgent.Mode = "retained"

	cfg.Agent.SubAgent.Containment = ""
	if c := CheckContainment(cfg, nil); c.Status != "OK" {
		t.Errorf("default containment: status = %q, want OK (%s)", c.Status, c.Detail)
	}

	cfg.Agent.SubAgent.Containment = "paddock"
	c := CheckContainment(cfg, nil)
	want := "FAIL"
	if landlock.Detect() != nil {
		want = "OK"
	}
	if c.Status != want {
		t.Errorf("paddock: status = %q, want %q (%s)", c.Status, want, c.Detail)
	}

	if c := CheckContainment(nil, errors.New("boom")); c.Status != "WARN" {
		t.Errorf("config error: status = %q, want WARN", c.Status)
	}
}
