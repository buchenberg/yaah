package runner

import (
	"testing"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

// The workdir lane attaches a confiner only when agents.subagent.containment
// asks for one, and records the resolution either way (Plan 7 Phase 3). The
// default (and "off"/"advisory") leaves the host path — the guardrail that the
// orchestrator's lane and the unconfigured sub-agent stay unconfined.
func TestResolveSubAgentConfinement_Placement(t *testing.T) {
	pv := tools.NewPathValidator(t.TempDir(), false, nil)

	cases := []struct {
		setting string
		confine bool // a confined workspace is returned
		record  bool // a resolution record is returned
	}{
		{"", false, false},
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
	// two apart.
	ws, rec := resolveSubAgentConfinement(pv, config.SubAgentConfig{Containment: "auto"}, t.TempDir())
	wantJail := landlock.Detect() != nil
	if (ws != nil) != wantJail {
		t.Errorf("auto: confined=%v, want %v", ws != nil, wantJail)
	}
	if rec == nil {
		t.Fatal("auto must record a resolution")
	}
	wantResolved := "advisory"
	if wantJail {
		wantResolved = "paddock"
	}
	if rec.Resolved != wantResolved {
		t.Errorf("auto: resolved = %q, want %q", rec.Resolved, wantResolved)
	}
	if wantJail && (rec.Backend == "" || rec.Tier == "") {
		t.Errorf("auto (jailed): backend/tier must be recorded, got %q / %q", rec.Backend, rec.Tier)
	}
}
