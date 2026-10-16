package runner

import (
	"testing"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

// The workdir lane attaches a confiner only when agents.subagent.containment
// asks for one. The default (and "off"/"advisory") leaves the host path — this
// is the guardrail that the orchestrator's lane and the unconfigured sub-agent
// stay unconfined.
func TestConfinedSubAgentWorkspace_Placement(t *testing.T) {
	pv := tools.NewPathValidator(t.TempDir(), false, nil)

	cases := []struct {
		setting string
		want    bool
	}{
		{"", false},
		{"off", false},
		{"none", false},
		{"advisory", false},
		{"paddock", true}, // fails closed even with no backend
		{"jail", true},
	}
	for _, c := range cases {
		got := confinedSubAgentWorkspace(pv, config.SubAgentConfig{Containment: c.setting}, t.TempDir())
		if (got != nil) != c.want {
			t.Errorf("containment=%q: confined=%v, want %v", c.setting, got != nil, c.want)
		}
	}

	// "auto" jails only where a backend exists; elsewhere it degrades to the
	// host (advisory) path, so the result tracks the host's capability.
	auto := confinedSubAgentWorkspace(pv, config.SubAgentConfig{Containment: "auto"}, t.TempDir())
	wantAuto := landlock.Detect() != nil
	if (auto != nil) != wantAuto {
		t.Errorf("containment=auto: confined=%v, want %v (backend present=%v)", auto != nil, wantAuto, wantAuto)
	}
}
