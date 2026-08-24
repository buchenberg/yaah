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
