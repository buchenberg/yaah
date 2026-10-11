package config

import "fmt"

// SupervisorConfig configures the supervision rules engine
// (shepherd-adoption plan B1). The stanza exists only when tracing is
// on: without `shepherd_trace_dir` neither the stanza nor anything it
// gates is consulted.
//
// Defaults, precisely: with tracing on and the stanza absent, every
// advisory rule is enabled with its built-in thresholds. Disables and
// thresholds are therefore expressed as deviations — a nil rule switch
// means enabled, a zero threshold means the rule's default.
type SupervisorConfig struct {
	// Enabled is the master switch for the advisory rules engine. Nil
	// (key unset) means on: tracing is already the opt-in, and the rules
	// exist to make supervision automatic. Explicit false disables the
	// whole engine — no rule fires, no intervention is bridged.
	Enabled *bool `yaml:"enabled"`

	// HaltRoot opts into supervisor halt of the session's root scope: a
	// deliberate stop of the session's current run, the ctrl-C
	// equivalent. Nil (unset) falls back to the A3 legacy flat key
	// (Defaults.SupervisorHaltRoot) — see HaltRootEnabled; an explicit
	// value wins over the legacy key in both directions. Off by
	// default everywhere — a rules engine or model halting the
	// interactive session is a big hammer. Root inject needs no gate:
	// steering the session is reversible.
	HaltRoot *bool `yaml:"halt_root"`

	// Rules configures the individual advisory rules. A nil rule is
	// enabled with its defaults.
	Rules SupervisorRulesConfig `yaml:"rules"`
}

// SupervisorRulesConfig holds the per-rule switches of the advisory
// rules engine. The blocking destructive guard is a separate, later
// step of the plan (B2) and lands with its own config then.
type SupervisorRulesConfig struct {
	// DestructiveTool warns on destructive tool declarations (advisory
	// inject-only; the blocking guard is B2). Nil = enabled.
	DestructiveTool *SupervisorRuleSwitch `yaml:"destructive_tool"`

	// HighErrorRate injects guidance when a scope's recent tool error
	// rate crosses the threshold. Nil = enabled with defaults.
	HighErrorRate *SupervisorHighErrorRateConfig `yaml:"high_error_rate"`

	// StuckDetection injects guidance when a scope repeats the same tool
	// call in a row. Nil = enabled with defaults.
	StuckDetection *SupervisorStuckDetectionConfig `yaml:"stuck_detection"`
}

// SupervisorRuleSwitch enables or disables one rule. Nil Enabled means
// enabled — the same deviation-not-permission shape as the master
// switch.
type SupervisorRuleSwitch struct {
	Enabled *bool `yaml:"enabled"`
}

// SupervisorHighErrorRateConfig carries the error-rate rule's switch
// and thresholds. Zero values mean the rule's defaults (threshold 0.5,
// window 10).
type SupervisorHighErrorRateConfig struct {
	Enabled   *bool   `yaml:"enabled"`
	Threshold float64 `yaml:"threshold"`
	Window    int     `yaml:"window"`
}

// SupervisorStuckDetectionConfig carries the stuck-detection rule's
// switch and threshold. Zero values mean the rule's defaults (repeat
// threshold 3).
type SupervisorStuckDetectionConfig struct {
	Enabled         *bool `yaml:"enabled"`
	RepeatThreshold int   `yaml:"repeat_threshold"`
}

// EngineEnabled reports whether the advisory rules engine runs at all:
// the master switch is off only when it was set to false explicitly.
func (s SupervisorConfig) EngineEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// DestructiveRuleEnabled reports the advisory destructive rule's
// switch, defaulting to enabled when unconfigured.
func (s SupervisorConfig) DestructiveRuleEnabled() bool {
	return s.Rules.DestructiveTool == nil || s.Rules.DestructiveTool.Enabled == nil || *s.Rules.DestructiveTool.Enabled
}

// HighErrorRateEnabled reports the error-rate rule's switch, defaulting
// to enabled when unconfigured.
func (s SupervisorConfig) HighErrorRateEnabled() bool {
	return s.Rules.HighErrorRate == nil || s.Rules.HighErrorRate.Enabled == nil || *s.Rules.HighErrorRate.Enabled
}

// StuckDetectionEnabled reports the stuck-detection rule's switch,
// defaulting to enabled when unconfigured.
func (s SupervisorConfig) StuckDetectionEnabled() bool {
	return s.Rules.StuckDetection == nil || s.Rules.StuckDetection.Enabled == nil || *s.Rules.StuckDetection.Enabled
}

// ErrorRateThreshold reports the error-rate rule's configured
// threshold, zero (the kernel rule's own default) when unconfigured.
func (s SupervisorConfig) ErrorRateThreshold() float64 {
	if s.Rules.HighErrorRate == nil {
		return 0
	}
	return s.Rules.HighErrorRate.Threshold
}

// ErrorRateWindow reports the error-rate rule's configured window,
// zero (the kernel rule's own default) when unconfigured.
func (s SupervisorConfig) ErrorRateWindow() int {
	if s.Rules.HighErrorRate == nil {
		return 0
	}
	return s.Rules.HighErrorRate.Window
}

// StuckRepeatThreshold reports the stuck-detection rule's configured
// threshold, zero (the kernel rule's own default) when unconfigured.
func (s SupervisorConfig) StuckRepeatThreshold() int {
	if s.Rules.StuckDetection == nil {
		return 0
	}
	return s.Rules.StuckDetection.RepeatThreshold
}

// HaltRootEnabled resolves the root-halt opt-in. The stanza's explicit
// value wins in both directions; unset falls back to the A3 legacy
// flat key, so existing configurations keep gating the same switch
// and the stanza remains the one home going forward.
func (d Defaults) HaltRootEnabled() bool {
	if d.Supervisor.HaltRoot != nil {
		return *d.Supervisor.HaltRoot
	}
	return d.SupervisorHaltRoot
}

// validateSupervisor checks the supervisor stanza's numeric settings.
// Zeros are the documented defaults; an explicit value must be usable
// by the rule it tunes — an error-rate threshold above 1 would make an
// enabled rule impossible to trigger, and negatives are neither
// defaults nor thresholds.
//
// The check follows the stanza's own contract: it exists only when
// tracing is on, and only for rules that are actually enabled — values
// the engine never reads (tracing off, engine off, rule off) cannot
// reject an otherwise valid config.
func validateSupervisor(d Defaults) []string {
	if d.ShepherdTraceDir == "" || !d.Supervisor.EngineEnabled() {
		return nil
	}
	sup := d.Supervisor
	var errs []string
	if sup.HighErrorRateEnabled() {
		if t := sup.ErrorRateThreshold(); t < 0 || t > 1 {
			errs = append(errs, fmt.Sprintf(
				"agents.default.supervisor.rules.high_error_rate.threshold %g must be 0 (default) or within (0, 1]", t))
		}
		if w := sup.ErrorRateWindow(); w < 0 {
			errs = append(errs, fmt.Sprintf(
				"agents.default.supervisor.rules.high_error_rate.window %d must be 0 (default) or positive", w))
		}
	}
	if sup.StuckDetectionEnabled() {
		if r := sup.StuckRepeatThreshold(); r < 0 {
			errs = append(errs, fmt.Sprintf(
				"agents.default.supervisor.rules.stuck_detection.repeat_threshold %d must be 0 (default) or positive", r))
		}
	}
	return errs
}
