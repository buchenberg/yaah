package config

import (
	"strings"
	"testing"
)

// Plan 7 Phase 1/2: the sub-agent containment placement and dispatch mode are
// validated, so a typo fails loudly instead of silently meaning "off"/"retained".
func TestValidateSubAgentContainment(t *testing.T) {
	for _, ok := range []string{"", "off", "none", "advisory", "auto", "paddock", "jail"} {
		cfg := defaultConfig()
		cfg.Agent.SubAgent.Containment = ok
		if err := Validate(cfg); err != nil {
			t.Errorf("containment %q should be valid: %v", ok, err)
		}
	}
	cfg := defaultConfig()
	cfg.Agent.SubAgent.Containment = "banana"
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "containment") {
		t.Fatalf("invalid containment should fail with a containment error, got %v", err)
	}
}

func TestValidateSubAgentMode(t *testing.T) {
	for _, ok := range []string{"", "retained", "shared"} {
		cfg := defaultConfig()
		cfg.Agent.SubAgent.Mode = ok
		if err := Validate(cfg); err != nil {
			t.Errorf("mode %q should be valid: %v", ok, err)
		}
	}
	cfg := defaultConfig()
	cfg.Agent.SubAgent.Mode = "banana"
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("invalid mode should fail with a mode error, got %v", err)
	}
}
