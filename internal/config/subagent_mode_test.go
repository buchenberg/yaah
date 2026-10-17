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

// Plan 7 Phase 3: the egress posture is validated, and an allowlist under a mode
// that would not use it is refused — a setting that looks like a policy but is
// not in effect is worse than no setting.
func TestValidateSubAgentNetwork(t *testing.T) {
	for _, ok := range []string{"", "allow_all", "deny_all", "broker"} {
		cfg := defaultConfig()
		cfg.Agent.SubAgent.Network = ok
		if ok == "broker" {
			cfg.Agent.SubAgent.NetworkHosts = []string{"api.example.com"}
		}
		if err := Validate(cfg); err != nil {
			t.Errorf("network %q should be valid: %v", ok, err)
		}
	}

	cfg := defaultConfig()
	cfg.Agent.SubAgent.Network = "borker"
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "network") {
		t.Fatalf("invalid network should fail with a network error, got %v", err)
	}

	// Hosts without the broker mode: the allowlist would do nothing.
	cfg = defaultConfig()
	cfg.Agent.SubAgent.Network = "deny_all"
	cfg.Agent.SubAgent.NetworkHosts = []string{"api.example.com"}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "network_hosts") {
		t.Fatalf("hosts under a non-broker mode should fail, got %v", err)
	}

	// An empty host entry is a typo, not an allowlist.
	cfg = defaultConfig()
	cfg.Agent.SubAgent.Network = "broker"
	cfg.Agent.SubAgent.NetworkHosts = []string{"api.example.com", "  "}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "network_hosts[1]") {
		t.Fatalf("an empty host entry should fail, got %v", err)
	}
}
