package config

import (
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// TestSupervisorDefaults pins the deviation-not-permission shape: with
// tracing on and the stanza absent, the engine and every advisory rule
// are enabled, thresholds fall to the kernel's own defaults (zero
// here), and halt_root stays off everywhere.
func TestSupervisorDefaults(t *testing.T) {
	writeConfig(t, `
agents:
  default:
    shepherd_trace_dir: ~/traces
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sup := cfg.Agent.Default.Supervisor
	if !sup.EngineEnabled() {
		t.Error("EngineEnabled = false, want true (tracing is the opt-in)")
	}
	if !sup.DestructiveRuleEnabled() || !sup.HighErrorRateEnabled() || !sup.StuckDetectionEnabled() {
		t.Error("absent rules must default to enabled")
	}
	if sup.HaltRoot {
		t.Error("HaltRoot must default off")
	}
	if sup.ErrorRateThreshold() != 0 || sup.ErrorRateWindow() != 0 || sup.StuckRepeatThreshold() != 0 {
		t.Error("absent thresholds must pass through as zero (the kernel rule defaults)")
	}
}

// TestSupervisorStanza pins the parse: master disable, per-rule
// switches, thresholds, and the nested halt_root.
func TestSupervisorStanza(t *testing.T) {
	writeConfig(t, `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor:
      enabled: false
      halt_root: true
      rules:
        destructive_tool: { enabled: false }
        high_error_rate:  { enabled: true, threshold: 0.9, window: 20 }
        stuck_detection:  { enabled: true, repeat_threshold: 5 }
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sup := cfg.Agent.Default.Supervisor
	if sup.EngineEnabled() {
		t.Error("explicit enabled: false must disable the engine")
	}
	if sup.DestructiveRuleEnabled() {
		t.Error("destructive_tool disabled by config")
	}
	if !sup.HighErrorRateEnabled() {
		t.Error("high_error_rate enabled by config")
	}
	if sup.ErrorRateThreshold() != 0.9 || sup.ErrorRateWindow() != 20 {
		t.Errorf("error-rate tuning = %f/%d, want 0.9/20", sup.ErrorRateThreshold(), sup.ErrorRateWindow())
	}
	if sup.StuckRepeatThreshold() != 5 {
		t.Errorf("stuck threshold = %d, want 5", sup.StuckRepeatThreshold())
	}
	if !sup.HaltRoot {
		t.Error("nested halt_root: true must parse")
	}
}

// TestSupervisorLegacyHaltRoot pins the A3 flat key's backward
// compatibility: supervisor_halt_root folds into the stanza's
// HaltRoot, so existing configurations keep gating the same switch.
func TestSupervisorLegacyHaltRoot(t *testing.T) {
	writeConfig(t, `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor_halt_root: true
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Agent.Default.Supervisor.HaltRoot {
		t.Error("the flat supervisor_halt_root must fold into Supervisor.HaltRoot")
	}
}

// TestSupervisorRuleSwitchResolve pins the *bool switch resolution
// directly: nil (unset) means enabled, false means disabled.
func TestSupervisorRuleSwitchResolve(t *testing.T) {
	var s SupervisorConfig
	if !s.DestructiveRuleEnabled() {
		t.Error("nil switch must resolve enabled")
	}
	s.Rules.DestructiveTool = &SupervisorRuleSwitch{Enabled: boolPtr(false)}
	if s.DestructiveRuleEnabled() {
		t.Error("explicit false must disable")
	}
	s.Enabled = boolPtr(false)
	if s.EngineEnabled() {
		t.Error("explicit master disable must disable")
	}
}
