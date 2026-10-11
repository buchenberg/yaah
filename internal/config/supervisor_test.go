package config

import (
	"strings"
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
	if cfg.Agent.Default.HaltRootEnabled() {
		t.Error("HaltRootEnabled must default off")
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
	if !cfg.Agent.Default.HaltRootEnabled() {
		t.Error("nested halt_root: true must parse and enable")
	}
}

// TestSupervisorHaltRootPrecedence pins the A3 flat key's backward
// compatibility and the stanza's win: unset in the stanza, the legacy
// supervisor_halt_root keeps gating the same switch; an explicit
// stanza value overrides the flat key in both directions.
func TestSupervisorHaltRootPrecedence(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		enabled bool
	}{
		{
			name: "legacy key alone still enables",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor_halt_root: true
`,
			enabled: true,
		},
		{
			name: "stanza false overrides legacy true",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor_halt_root: true
    supervisor:
      halt_root: false
`,
			enabled: false,
		},
		{
			name: "stanza true enables without the legacy key",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor:
      halt_root: true
`,
			enabled: true,
		},
		{
			name: "both unset stay off",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
`,
			enabled: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeConfig(t, tc.config)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Agent.Default.HaltRootEnabled(); got != tc.enabled {
				t.Errorf("HaltRootEnabled = %v, want %v", got, tc.enabled)
			}
		})
	}
}

// TestSupervisorThresholdValidation pins that explicit rule tuning
// must be usable by the rule it tunes: thresholds outside the usable
// range are refused at load, not discovered as silent no-ops (or
// always-on triggers) mid-session.
func TestSupervisorThresholdValidation(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name: "threshold above 1 can never trigger",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor:
      rules:
        high_error_rate: {threshold: 1.5}
`,
			wantErr: "high_error_rate.threshold 1.5",
		},
		{
			name: "negative threshold",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor:
      rules:
        high_error_rate: {threshold: -0.1}
`,
			wantErr: "high_error_rate.threshold -0.1",
		},
		{
			name: "negative window",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor:
      rules:
        high_error_rate: {window: -5}
`,
			wantErr: "high_error_rate.window -5",
		},
		{
			name: "negative repeat threshold",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor:
      rules:
        stuck_detection: {repeat_threshold: -1}
`,
			wantErr: "stuck_detection.repeat_threshold -1",
		},
		{
			name: "a valid tuning passes",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor:
      rules:
        high_error_rate: {threshold: 0.9, window: 20}
        stuck_detection: {repeat_threshold: 5}
`,
		},
		{
			// The stanza's contract: without a trace dir it is
			// inert, so values the engine never reads cannot
			// reject the config.
			name: "tracing off makes the stanza inert",
			config: `
agents:
  default:
    supervisor:
      rules:
        high_error_rate: {threshold: 1.5}
`,
		},
		{
			name: "engine off makes the stanza inert",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor:
      enabled: false
      rules:
        high_error_rate: {threshold: 1.5}
`,
		},
		{
			name: "a disabled rule's threshold is never read",
			config: `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor:
      rules:
        high_error_rate: {enabled: false, threshold: 1.5}
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeConfig(t, tc.config)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			verr := Validate(cfg)
			if tc.wantErr == "" {
				if verr != nil {
					t.Fatalf("Validate = %v, want no errors", verr)
				}
				return
			}
			if verr == nil || !strings.Contains(verr.Error(), tc.wantErr) {
				t.Errorf("Validate = %v, want %q named", verr, tc.wantErr)
			}
		})
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

// TestSupervisorDestructiveGuardSwitch pins the blocking guard's
// switch (adoption plan B2): absent means enabled — the plan's §7
// default — and an explicit false disables it.
func TestSupervisorDestructiveGuardSwitch(t *testing.T) {
	writeConfig(t, `
agents:
  default:
    shepherd_trace_dir: ~/traces
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Agent.Default.Supervisor.DestructiveGuardEnabled() {
		t.Error("absent destructive_guard must default to enabled")
	}

	writeConfig(t, `
agents:
  default:
    shepherd_trace_dir: ~/traces
    supervisor:
      rules:
        destructive_guard: {enabled: false}
`)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.Default.Supervisor.DestructiveGuardEnabled() {
		t.Error("explicit destructive_guard: false must disable")
	}
}
