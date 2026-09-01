package config

import (
	"strings"
	"testing"
)

func TestValidateDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if err := Validate(cfg); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
}

func TestValidateNilConfig(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Fatal("nil config should fail validation")
	}
}

func TestValidateInvalidApproval(t *testing.T) {
	cfg := defaultConfig()
	cfg.Agent.Default.Approval = "banana"
	err := Validate(cfg)
	if err == nil {
		t.Fatal("invalid approval mode should fail")
	}
	if !strings.Contains(err.Error(), "approval") {
		t.Errorf("expected approval error, got %v", err)
	}
}

func TestValidateCompactionThresholdRange(t *testing.T) {
	cfg := defaultConfig()
	cfg.Agent.Default.CompactionThreshold = 1.5
	err := Validate(cfg)
	if err == nil {
		t.Fatal("compaction threshold > 1 should fail")
	}
	if !strings.Contains(err.Error(), "compaction_threshold") {
		t.Errorf("expected compaction_threshold error, got %v", err)
	}
}

func TestValidateNegativeContextWindow(t *testing.T) {
	cfg := defaultConfig()
	cfg.Agent.Default.ContextWindow = -1
	err := Validate(cfg)
	if err == nil {
		t.Fatal("negative context window should fail")
	}
	if !strings.Contains(err.Error(), "context_window") {
		t.Errorf("expected context_window error, got %v", err)
	}
}

// TestValidateMalformedDenyPattern pins that invalid workspace deny
// globs are rejected at startup: a malformed pattern fails open at
// match time and would silently disable the protection it configured.
func TestValidateMalformedDenyPattern(t *testing.T) {
	cfg := defaultConfig()
	cfg.Agent.Default.WorkspaceDenyPatterns = []string{"*.pem", "["}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("malformed deny pattern should fail validation")
	}
	if !strings.Contains(err.Error(), "workspace_deny_patterns") {
		t.Errorf("expected workspace_deny_patterns error, got %v", err)
	}
}

func TestValidateFallbackProviderMissing(t *testing.T) {
	cfg := defaultConfig()
	cfg.Agent.Fallback.Provider = "nonexistent"
	err := Validate(cfg)
	if err == nil {
		t.Fatal("fallback provider not in providers map should fail")
	}
	if !strings.Contains(err.Error(), "fallback") {
		t.Errorf("expected fallback error, got %v", err)
	}
}

func TestValidateSubAgentProviderMissing(t *testing.T) {
	cfg := defaultConfig()
	cfg.Agent.SubAgent.Provider = "ghost"
	err := Validate(cfg)
	if err == nil {
		t.Fatal("subagent provider not in providers map should fail")
	}
	if !strings.Contains(err.Error(), "subagent") {
		t.Errorf("expected subagent error, got %v", err)
	}
}

// Budget floor validation (subagent-turn-budget-floors §4.6): config
// floors are hard-validated at startup because a silently unsatisfiable
// floor would reproduce the starvation it exists to prevent.
func TestValidateSubAgentBudgetFloors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{
			name:    "negative role min_turns",
			mutate:  func(c *Config) { c.Agent.SubAgent.Roles = map[string]RoleConfig{"r": {MinToolTurns: -2}} },
			wantSub: "min_turns must not be negative",
		},
		{
			name:    "negative role min_iterations",
			mutate:  func(c *Config) { c.Agent.SubAgent.Roles = map[string]RoleConfig{"r": {MinLoopCycles: -1}} },
			wantSub: "min_iterations must not be negative",
		},
		{
			name: "role min_turns above max_turns",
			mutate: func(c *Config) {
				c.Agent.SubAgent.Roles = map[string]RoleConfig{"r": {MinToolTurns: 10, MaxToolTurns: 4}}
			},
			wantSub: "min_turns 10 exceeds max_turns 4",
		},
		{
			name: "role min_iterations above max_iterations",
			mutate: func(c *Config) {
				c.Agent.SubAgent.Roles = map[string]RoleConfig{"r": {MinLoopCycles: 30, MaxLoopCycles: 12}}
			},
			wantSub: "min_iterations 30 exceeds max_iterations 12",
		},
		{
			name:    "role min_turns unsatisfiable within hard ceiling",
			mutate:  func(c *Config) { c.Agent.SubAgent.Roles = map[string]RoleConfig{"r": {MinToolTurns: 50}} },
			wantSub: "unsatisfiable",
		},
		{
			name:    "negative global default_min_turns",
			mutate:  func(c *Config) { c.Agent.SubAgent.DefaultMinToolTurns = -3 },
			wantSub: "default_min_turns must not be negative",
		},
		{
			name:    "global default_min_turns unsatisfiable",
			mutate:  func(c *Config) { c.Agent.SubAgent.DefaultMinToolTurns = 60 },
			wantSub: "unsatisfiable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultConfig()
			tc.mutate(cfg)
			err := Validate(cfg)
			if err == nil {
				t.Fatal("invalid budget floor should fail validation")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %v, want substring %q", err, tc.wantSub)
			}
		})
	}

	t.Run("sane floors pass", func(t *testing.T) {
		cfg := defaultConfig()
		cfg.Agent.SubAgent.DefaultMinToolTurns = 4
		cfg.Agent.SubAgent.Roles = map[string]RoleConfig{
			"r": {MinToolTurns: 4, MaxToolTurns: 10, MinLoopCycles: 8, MaxLoopCycles: 30},
		}
		if err := Validate(cfg); err != nil {
			t.Fatalf("sane floors should validate: %v", err)
		}
	})
}

func TestValidateMCPServerNoCommandOrURL(t *testing.T) {
	cfg := defaultConfig()
	cfg.MCPServers = map[string]MCPServerConfig{
		"broken": {},
	}
	// Missing command/url is NOT a hard error — it's a no-op at load time.
	if err := Validate(cfg); err != nil {
		t.Fatalf("degenerate MCP entry should not fail validation: %v", err)
	}
}

func TestValidateMCPServerInvalidTransport(t *testing.T) {
	cfg := defaultConfig()
	cfg.MCPServers = map[string]MCPServerConfig{
		"bad": {Command: "echo", Transport: "carrier-pigeon"},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("MCP server with invalid transport should fail")
	}
	if !strings.Contains(err.Error(), "transport") {
		t.Errorf("expected transport error, got %v", err)
	}
}

func TestValidateMultipleErrors(t *testing.T) {
	cfg := defaultConfig()
	cfg.Agent.Default.Approval = "banana"
	cfg.Agent.Default.ContextWindow = -1
	cfg.Agent.Fallback.Provider = "ghost"
	err := Validate(cfg)
	if err == nil {
		t.Fatal("multiple errors should fail")
	}
	ve, ok := err.(ValidationError)
	if !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if len(ve.Errors) < 3 {
		t.Fatalf("expected at least 3 errors, got %d: %v", len(ve.Errors), ve.Errors)
	}
}
