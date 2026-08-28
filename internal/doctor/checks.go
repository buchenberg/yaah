package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/config"
)

// Check represents a single diagnostic result.
type Check struct {
	Label  string
	Detail string
	Status string // "OK", "WARN", "FAIL"
}

// Options holds configuration for RunChecks. Provides explicit state
// that was previously passed via package-level globals.
type Options struct {
	DirectiveOverrides []string
}

// doctorUseColor is set based on NO_COLOR environment variable.
var doctorUseColor = os.Getenv("NO_COLOR") == ""

// RunChecks executes all diagnostic checks and returns the results.
func RunChecks(opts Options) []Check {
	cfg, cfgErr := config.Load()
	cfgPath, pathErr := config.ConfigPath()

	return []Check{
		CheckConfigFile(cfgPath, cfg, cfgErr, pathErr),
		CheckOldConfig(cfgPath),
		CheckProvider(cfg, cfgErr),
		CheckModel(cfg, cfgErr),
		CheckSubAgents(cfg, cfgErr),
		CheckFallback(cfg, cfgErr),
		CheckQualityGates(cfg, cfgErr),
		CheckDirectives(cfg, cfgErr, opts.DirectiveOverrides),
		CheckPipeline(cfg, cfgErr),
		CheckOTel(cfg, cfgErr),
		CheckHomeWritable(),
		CheckPlatform(),
		CheckEditor(cfg),
	}
}

// AllOK returns true if all checks have status "OK".
func AllOK(checks []Check) bool {
	for _, c := range checks {
		if c.Status != "OK" {
			return false
		}
	}
	return true
}

// CheckConfigFile checks the configuration file existence and validity.
func CheckConfigFile(path string, cfg *config.Config, cfgErr, pathErr error) Check {
	if pathErr != nil {
		return Check{Label: "Config path", Status: "FAIL", Detail: pathErr.Error()}
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return Check{
			Label:  "Config file",
			Detail: fmt.Sprintf("not found at %s — using built-in defaults", path),
			Status: "WARN",
		}
	}
	if cfgErr != nil {
		return Check{Label: "Config file", Status: "FAIL", Detail: cfgErr.Error()}
	}
	detail := fmt.Sprintf("%s (model: %s, %d provider(s))", path, cfg.Agent.Default.Model, len(cfg.Providers))
	return Check{Label: "Config file", Status: "OK", Detail: detail}
}

// CheckOldConfig checks if the config file uses the old format.
func CheckOldConfig(path string) Check {
	if path == "" {
		return Check{Label: "Config format", Status: "OK", Detail: "no config file"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Check{Label: "Config format", Status: "WARN", Detail: fmt.Sprintf("cannot read: %v", err)}
	}
	if config.HasOldConfig(data) {
		return Check{
			Label:  "Config format",
			Status: "WARN",
			Detail: "old-style config detected — rename 'default:' to 'agents: default:' and 'agent:' to 'agents:'",
		}
	}
	return Check{Label: "Config format", Status: "OK", Detail: "agents.* format"}
}

// CheckProvider checks the provider configuration.
func CheckProvider(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Providers", Status: "WARN", Detail: "config not loaded"}
	}
	if len(cfg.Providers) == 0 {
		return Check{Label: "Providers", Status: "FAIL", Detail: "no providers configured — add at least one provider in config.yaml"}
	}

	providerName := cfg.Agent.Default.Provider
	if providerName == "" {
		providerName = resolveProviderName(cfg)
	}

	if _, ok := cfg.Providers[providerName]; !ok {
		return Check{
			Label:  "Default provider",
			Status: "WARN",
			Detail: fmt.Sprintf("no provider key %q found — check default.provider or set a provider prefix on default.model", providerName),
		}
	}

	var issues []string
	for name, p := range cfg.Providers {
		if p.APIKey == "" || p.APIKey == "ollama" {
			continue
		}
		if strings.HasPrefix(p.APIKey, "${") && strings.HasSuffix(p.APIKey, "}") {
			envVar := p.APIKey[2 : len(p.APIKey)-1]
			if os.Getenv(envVar) == "" {
				issues = append(issues, fmt.Sprintf("%s: env var %s is empty or unset", name, envVar))
			}
		}
	}

	if len(issues) > 0 {
		return Check{
			Label:  "Provider API keys",
			Status: "WARN",
			Detail: strings.Join(issues, "; "),
		}
	}

	return Check{Label: "Providers", Status: "OK", Detail: fmt.Sprintf("%d configured, default is %q", len(cfg.Providers), providerName)}
}

// CheckModel checks the default model configuration.
func CheckModel(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Default model", Status: "WARN", Detail: "config not loaded"}
	}
	if cfg.Agent.Default.Model == "" {
		return Check{Label: "Default model", Status: "FAIL", Detail: "default.model is not set"}
	}
	return Check{Label: "Default model", Status: "OK", Detail: cfg.Agent.Default.Model}
}

// CheckSubAgents reports the sub-agent provider/model configuration.
func CheckSubAgents(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Sub-agents", Status: "WARN", Detail: "config not loaded"}
	}
	sc := cfg.Agent.SubAgent
	providerName := sc.Provider
	model := sc.Model
	if model == "" {
		model = cfg.Agent.Default.Model
	}
	if providerName == "" {
		providerName = resolveProviderName(cfg)
		return Check{
			Label:  "Sub-agents",
			Status: "OK",
			Detail: fmt.Sprintf("enabled — inherit planner model (%s / %s, max depth 1, max concurrency %d)", providerName, model, sc.MaxConcurrency),
		}
	}
	if _, ok := cfg.Providers[providerName]; !ok {
		return Check{
			Label:  "Sub-agents",
			Status: "WARN",
			Detail: fmt.Sprintf("provider %q not found for sub-agents — falling back to planner (%s / %s)", sc.Provider, providerName, model),
		}
	}
	return Check{
		Label:  "Sub-agents",
		Status: "OK",
		Detail: fmt.Sprintf("enabled — %s / %s, max depth 1, max concurrency %d", providerName, model, sc.MaxConcurrency),
	}
}

// CheckFallback reports the fallback provider/model configuration.
func CheckFallback(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Fallback", Status: "WARN", Detail: "config not loaded"}
	}
	fc := cfg.Agent.Fallback
	if fc.Provider == "" {
		return Check{Label: "Fallback", Status: "OK", Detail: "not configured"}
	}
	if _, ok := cfg.Providers[fc.Provider]; !ok {
		return Check{
			Label:  "Fallback",
			Status: "WARN",
			Detail: fmt.Sprintf("provider %q not found for fallback (%s / %s)", fc.Provider, fc.Provider, fc.Model),
		}
	}
	return Check{
		Label:  "Fallback",
		Status: "OK",
		Detail: fmt.Sprintf("%s / %s", fc.Provider, fc.Model),
	}
}

// CheckQualityGates reports configured quality gate validators.
func CheckQualityGates(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Quality gates", Status: "WARN", Detail: "config not loaded"}
	}
	gates := cfg.Agent.QualityGates
	if len(gates) == 0 {
		return Check{Label: "Quality gates", Status: "OK", Detail: "not configured"}
	}
	var parts []string
	for role, validators := range gates {
		if len(validators) > 0 {
			parts = append(parts, fmt.Sprintf("%s→[%s]", role, strings.Join(validators, ",")))
		}
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return Check{Label: "Quality gates", Status: "OK", Detail: "configured but no active gates"}
	}
	return Check{Label: "Quality gates", Status: "OK", Detail: strings.Join(parts, ", ")}
}

// CheckDirectives reports active session directives from config.
func CheckDirectives(cfg *config.Config, cfgErr error, overrides []string) Check {
	if cfgErr != nil {
		return Check{Label: "Directives", Status: "WARN", Detail: "config not loaded"}
	}
	directives := cfg.Agent.Default.Directives
	cliCount := len(overrides)
	if len(directives) == 0 && cliCount == 0 {
		return Check{Label: "Directives", Status: "OK", Detail: "none configured"}
	}
	var detail string
	if cliCount > 0 && len(directives) > 0 {
		detail = fmt.Sprintf("%d from CLI, %d from config", cliCount, len(directives))
	} else if cliCount > 0 {
		detail = fmt.Sprintf("%d from CLI", cliCount)
	} else {
		detail = fmt.Sprintf("%d from config", len(directives))
	}
	return Check{Label: "Directives", Status: "OK", Detail: detail}
}

// CheckPipeline reports the active middleware pipeline. The default
// set is read from the pipeline package instead of maintaining a local
// copy that drifted out of sync (review B10e). middleware.enabled is
// additive over the defaults; disabled removes from the union. Names
// without a registered builder (e.g. deleted middleware still listed
// in config) are flagged — the pipeline silently skips them at build
// time, so reporting them as active would be dishonest.
func CheckPipeline(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Pipeline", Status: "WARN", Detail: "config not loaded"}
	}
	mc := cfg.Agent.Middleware
	active := pipeline.ResolvedPipelineNames(mc.Enabled, mc.Disabled)

	registered := make(map[string]bool)
	for _, name := range pipeline.RegisteredOrchestratorNames() {
		registered[name] = true
	}
	var unknown []string
	unknownSet := make(map[string]bool)
	for _, name := range active {
		if !registered[name] && !unknownSet[name] {
			unknownSet[name] = true
			unknown = append(unknown, name)
		}
	}

	// The honest active list only carries registered middleware.
	reported := make([]string, 0, len(active))
	for _, name := range active {
		if !unknownSet[name] {
			reported = append(reported, name)
		}
	}

	detail := fmt.Sprintf("%d middleware active: %s", len(reported), strings.Join(reported, " → "))
	if len(mc.Disabled) > 0 {
		detail += fmt.Sprintf(" (disabled: %s)", strings.Join(mc.Disabled, ", "))
	}
	if len(unknown) > 0 {
		detail += fmt.Sprintf(" — WARN: config names unregistered middleware (silently skipped): %s", strings.Join(unknown, ", "))
		return Check{Label: "Pipeline", Status: "WARN", Detail: detail}
	}
	return Check{Label: "Pipeline", Status: "OK", Detail: detail}
}

// CheckOTel checks OpenTelemetry configuration.
func CheckOTel(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Observability", Status: "WARN", Detail: "config not loaded"}
	}
	if !cfg.Observability.Otel.Enabled {
		return Check{Label: "Observability", Status: "OK", Detail: "OTel disabled"}
	}
	endpoint := cfg.Observability.Otel.Endpoint
	if ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); ep != "" {
		endpoint = ep
	}
	if endpoint == "" {
		return Check{Label: "Observability", Status: "WARN", Detail: "OTel enabled but no endpoint set"}
	}
	parts := []string{fmt.Sprintf("endpoint → %s", endpoint)}
	if cfg.Observability.Otel.Verbose {
		parts = append(parts, "verbose")
	}
	if cfg.Observability.Otel.Traces {
		parts = append(parts, "traces: on")
	} else {
		parts = append(parts, "traces: off")
	}
	if cfg.Observability.Otel.Metrics {
		parts = append(parts, "metrics: on")
	}
	return Check{Label: "Observability", Status: "OK", Detail: strings.Join(parts, ", ")}
}

// CheckHomeWritable checks if the home directory is writable.
func CheckHomeWritable() Check {
	home := os.Getenv("YAAH_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Check{Label: "Home directory", Status: "FAIL", Detail: err.Error()}
		}
		home = filepath.Join(userHome, ".yaah")
	}

	if err := os.MkdirAll(home, 0o755); err != nil {
		return Check{Label: "Home directory writable", Status: "FAIL", Detail: err.Error()}
	}

	testFile := filepath.Join(home, ".doctor-write-test")
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		return Check{Label: "Home directory writable", Status: "FAIL", Detail: err.Error()}
	}
	os.Remove(testFile)

	return Check{Label: "Home directory writable", Status: "OK", Detail: home}
}

// CheckPlatform checks the platform information.
func CheckPlatform() Check {
	return Check{
		Label:  "Platform",
		Status: "OK",
		Detail: fmt.Sprintf("%s/%s %s", runtime.GOOS, runtime.GOARCH, runtime.Version()),
	}
}

// CheckEditor checks the editor configuration.
func CheckEditor(cfg *config.Config) Check {
	editor := config.ResolveEditor(cfg)

	// Determine the source for the detail line
	source := "default (vi)"
	if cfg != nil && cfg.Editor != "" {
		source = "config.yaml"
	} else if env := os.Getenv("EDITOR"); env != "" {
		source = "$EDITOR"
	} else if env := os.Getenv("VISUAL"); env != "" {
		source = "$VISUAL"
	}

	if editor == "vi" && source == "default (vi)" {
		return Check{
			Label:  "Editor",
			Detail: "not configured — set 'editor' in config.yaml or $EDITOR/$VISUAL (falling back to vi)",
			Status: "WARN",
		}
	}
	return Check{Label: "Editor", Status: "OK", Detail: fmt.Sprintf("%s (via %s)", editor, source)}
}

// resolveProviderName extracts the provider name from the config.
// This is a helper function used by multiple check functions.
func resolveProviderName(cfg *config.Config) string {
	// 1. Explicit default.provider setting
	if cfg.Agent.Default.Provider != "" {
		if _, ok := cfg.Providers[cfg.Agent.Default.Provider]; ok {
			return cfg.Agent.Default.Provider
		}
	}
	// 2. Provider/model prefix in default.model
	if parts := strings.SplitN(cfg.Agent.Default.Model, "/", 2); len(parts) == 2 {
		if _, ok := cfg.Providers[parts[0]]; ok {
			return parts[0]
		}
	}
	// 3. First provider alphabetically
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return "local"
}

// Color helper functions - these respect NO_COLOR environment variable

// StatusLabel returns the status string with appropriate color coding.
func StatusLabel(status string) string {
	if !doctorUseColor {
		return status
	}
	switch status {
	case "OK":
		return "\x1b[32m" + status + "\x1b[0m"
	case "WARN":
		return "\x1b[33m" + status + "\x1b[0m"
	case "FAIL":
		return "\x1b[31m" + status + "\x1b[0m"
	}
	return status
}

// GreenText returns the string in green color.
func GreenText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[32m" + s + "\x1b[0m"
}

// YellowText returns the string in yellow color.
func YellowText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[33m" + s + "\x1b[0m"
}

// DimText returns the string in dimmed color.
func DimText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}
