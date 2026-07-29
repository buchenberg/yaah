package yaah

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/spf13/cobra"
)

// doctorCmd runs diagnostic checks on the yaah installation.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose config, environment, and system health",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		checks := runChecks()
		for _, c := range checks {
			cmd.Printf("  [%s]  %s\n", statusLabel(c.Status), c.Label)
			if c.Detail != "" {
				cmd.Printf("         %s\n", dimText(c.Detail))
			}
		}

		cmd.Println()
		if allOK(checks) {
			cmd.Println(greenText("All checks passed. yaah is ready."))
		} else {
			cmd.Println(yellowText("Some checks need attention."))
		}

		return nil
	},
}

// check represents a single diagnostic result.
type check struct {
	Label  string
	Detail string
	Status string // "OK", "WARN", "FAIL"
}

// runChecks executes all diagnostic checks and returns the results.
func runChecks() []check {
	cfg, cfgErr := config.Load()
	cfgPath, pathErr := config.ConfigPath()

	return []check{
		checkConfigFile(cfgPath, cfg, cfgErr, pathErr),
		checkOldConfig(cfgPath),
		checkProvider(cfg, cfgErr),
		checkModel(cfg, cfgErr),
		checkSubAgents(cfg, cfgErr),
		checkQualityGates(cfg, cfgErr),
		checkDirectives(cfg, cfgErr),
		checkPipeline(cfg, cfgErr),
		checkFallback(cfg, cfgErr),
		checkOTel(cfg, cfgErr),
		checkHomeWritable(),
		checkPlatform(),
		checkEditor(cfg),
	}
}

func allOK(checks []check) bool {
	for _, c := range checks {
		if c.Status != "OK" {
			return false
		}
	}
	return true
}

func checkConfigFile(path string, cfg *config.Config, cfgErr, pathErr error) check {
	if pathErr != nil {
		return check{Label: "Config path", Status: "FAIL", Detail: pathErr.Error()}
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return check{
			Label:  "Config file",
			Detail: fmt.Sprintf("not found at %s — using built-in defaults", path),
			Status: "WARN",
		}
	}
	if cfgErr != nil {
		return check{Label: "Config file", Status: "FAIL", Detail: cfgErr.Error()}
	}
	detail := fmt.Sprintf("%s (model: %s, %d provider(s))", path, cfg.Agent.Default.Model, len(cfg.Providers))
	return check{Label: "Config file", Status: "OK", Detail: detail}
}

func checkOldConfig(path string) check {
	if path == "" {
		return check{Label: "Config format", Status: "OK", Detail: "no config file"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return check{Label: "Config format", Status: "OK", Detail: "cannot read"}
	}
	if config.HasOldConfig(data) {
		return check{
			Label:  "Config format",
			Status: "WARN",
			Detail: "old-style config detected — rename 'default:' to 'agents: default:' and 'agent:' to 'agents:'",
		}
	}
	return check{Label: "Config format", Status: "OK", Detail: "agents.* format"}
}

func checkProvider(cfg *config.Config, cfgErr error) check {
	if cfgErr != nil {
		return check{Label: "Providers", Status: "WARN", Detail: "config not loaded"}
	}
	if len(cfg.Providers) == 0 {
		return check{Label: "Providers", Status: "FAIL", Detail: "no providers configured — add at least one provider in config.yaml"}
	}

	providerName := cfg.Agent.Default.Provider
	if providerName == "" {
		providerName = resolveProviderName(cfg)
	}

	if _, ok := cfg.Providers[providerName]; !ok {
		return check{
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
		return check{
			Label:  "Provider API keys",
			Status: "WARN",
			Detail: strings.Join(issues, "; "),
		}
	}

	return check{Label: "Providers", Status: "OK", Detail: fmt.Sprintf("%d configured, default is %q", len(cfg.Providers), providerName)}
}

func checkModel(cfg *config.Config, cfgErr error) check {
	if cfgErr != nil {
		return check{Label: "Default model", Status: "WARN", Detail: "config not loaded"}
	}
	if cfg.Agent.Default.Model == "" {
		return check{Label: "Default model", Status: "FAIL", Detail: "default.model is not set"}
	}
	return check{Label: "Default model", Status: "OK", Detail: cfg.Agent.Default.Model}
}

// checkSubAgents reports the sub-agent provider/model configuration.
func checkSubAgents(cfg *config.Config, cfgErr error) check {
	if cfgErr != nil {
		return check{Label: "Sub-agents", Status: "WARN", Detail: "config not loaded"}
	}
	sc := cfg.Agent.SubAgent
	providerName := sc.Provider
	model := sc.Model
	if model == "" {
		model = cfg.Agent.Default.Model
	}
	if providerName == "" {
		providerName = resolveProviderName(cfg)
		return check{
			Label:  "Sub-agents",
			Status: "OK",
			Detail: fmt.Sprintf("enabled — inherit planner model (%s / %s, max depth 1, max concurrency %d)", providerName, model, sc.MaxConcurrency),
		}
	}
	if _, ok := cfg.Providers[providerName]; !ok {
		return check{
			Label:  "Sub-agents",
			Status: "WARN",
			Detail: fmt.Sprintf("provider %q not found for sub-agents — falling back to planner (%s / %s)", sc.Provider, providerName, model),
		}
	}
	return check{
		Label:  "Sub-agents",
		Status: "OK",
		Detail: fmt.Sprintf("enabled — %s / %s, max depth 1, max concurrency %d", providerName, model, sc.MaxConcurrency),
	}
}

// checkFallback reports the fallback provider/model configuration.
func checkFallback(cfg *config.Config, cfgErr error) check {
	if cfgErr != nil {
		return check{Label: "Fallback", Status: "WARN", Detail: "config not loaded"}
	}
	fc := cfg.Agent.Fallback
	if fc.Provider == "" {
		return check{Label: "Fallback", Status: "OK", Detail: "not configured"}
	}
	if _, ok := cfg.Providers[fc.Provider]; !ok {
		return check{
			Label:  "Fallback",
			Status: "WARN",
			Detail: fmt.Sprintf("provider %q not found for fallback (%s / %s)", fc.Provider, fc.Provider, fc.Model),
		}
	}
	return check{
		Label:  "Fallback",
		Status: "OK",
		Detail: fmt.Sprintf("%s / %s", fc.Provider, fc.Model),
	}
}

// checkQualityGates reports configured quality gate validators.
func checkQualityGates(cfg *config.Config, cfgErr error) check {
	if cfgErr != nil {
		return check{Label: "Quality gates", Status: "WARN", Detail: "config not loaded"}
	}
	gates := cfg.Agent.QualityGates
	if len(gates) == 0 {
		return check{Label: "Quality gates", Status: "OK", Detail: "not configured"}
	}
	var parts []string
	for role, validators := range gates {
		if len(validators) > 0 {
			parts = append(parts, fmt.Sprintf("%s→[%s]", role, strings.Join(validators, ",")))
		}
	}
	if len(parts) == 0 {
		return check{Label: "Quality gates", Status: "OK", Detail: "configured but no active gates"}
	}
	return check{Label: "Quality gates", Status: "OK", Detail: strings.Join(parts, ", ")}
}

// checkDirectives reports active session directives from config.
func checkDirectives(cfg *config.Config, cfgErr error) check {
	if cfgErr != nil {
		return check{Label: "Directives", Status: "WARN", Detail: "config not loaded"}
	}
	directives := cfg.Agent.Default.Directives
	cliCount := len(directiveOverrides)
	if len(directives) == 0 && cliCount == 0 {
		return check{Label: "Directives", Status: "OK", Detail: "none configured"}
	}
	var detail string
	if cliCount > 0 && len(directives) > 0 {
		detail = fmt.Sprintf("%d from CLI, %d from config", cliCount, len(directives))
	} else if cliCount > 0 {
		detail = fmt.Sprintf("%d from CLI", cliCount)
	} else {
		detail = fmt.Sprintf("%d from config", len(directives))
	}
	return check{Label: "Directives", Status: "OK", Detail: detail}
}

// checkPipeline reports the active middleware pipeline.
func checkPipeline(cfg *config.Config, cfgErr error) check {
	if cfgErr != nil {
		return check{Label: "Pipeline", Status: "WARN", Detail: "config not loaded"}
	}
	mc := cfg.Agent.Middleware
	if len(mc.Enabled) > 0 {
		return check{Label: "Pipeline", Status: "OK", Detail: fmt.Sprintf("explicit: %s", strings.Join(mc.Enabled, " → "))}
	}
	defaults := []string{"steer", "followup", "compaction", "soft_prune", "approval", "tool_concurrency", "loop_detection", "staleness"}
	active := make([]string, 0, len(defaults))
	disabled := make(map[string]bool, len(mc.Disabled))
	for _, d := range mc.Disabled {
		disabled[d] = true
	}
	for _, name := range defaults {
		if !disabled[name] {
			active = append(active, name)
		}
	}
	detail := fmt.Sprintf("%d middleware active", len(active))
	if len(mc.Disabled) > 0 {
		detail += fmt.Sprintf(" (disabled: %s)", strings.Join(mc.Disabled, ", "))
	}
	return check{Label: "Pipeline", Status: "OK", Detail: detail}
}

func checkOTel(cfg *config.Config, cfgErr error) check {
	if cfgErr != nil {
		return check{Label: "Observability", Status: "WARN", Detail: "config not loaded"}
	}
	if !cfg.Observability.Otel.Enabled {
		return check{Label: "Observability", Status: "OK", Detail: "OTel disabled"}
	}
	endpoint := cfg.Observability.Otel.Endpoint
	if ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); ep != "" {
		endpoint = ep
	}
	if endpoint == "" {
		return check{Label: "Observability", Status: "WARN", Detail: "OTel enabled but no endpoint set"}
	}
	parts := []string{fmt.Sprintf("traces → %s", endpoint)}
	if cfg.Observability.Otel.Verbose {
		parts = append(parts, "verbose")
	}
	if !cfg.Observability.Otel.Traces {
		parts = append(parts, "traces: off")
	}
	if cfg.Observability.Otel.Metrics {
		parts = append(parts, "metrics: on")
	}
	return check{Label: "Observability", Status: "OK", Detail: strings.Join(parts, ", ")}
}

func checkHomeWritable() check {
	home := os.Getenv("YAAH_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return check{Label: "Home directory", Status: "FAIL", Detail: err.Error()}
		}
		home = filepath.Join(userHome, ".yaah")
	}

	if err := os.MkdirAll(home, 0o755); err != nil {
		return check{Label: "Home directory writable", Status: "FAIL", Detail: err.Error()}
	}

	testFile := filepath.Join(home, ".doctor-write-test")
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		return check{Label: "Home directory writable", Status: "FAIL", Detail: err.Error()}
	}
	os.Remove(testFile)

	return check{Label: "Home directory writable", Status: "OK", Detail: home}
}

func checkPlatform() check {
	return check{
		Label:  "Platform",
		Status: "OK",
		Detail: fmt.Sprintf("%s/%s %s", runtime.GOOS, runtime.GOARCH, runtime.Version()),
	}
}

func checkEditor(cfg *config.Config) check {
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
		return check{
			Label:  "Editor",
			Detail: "not configured — set 'editor' in config.yaml or $EDITOR/$VISUAL (falling back to vi)",
			Status: "WARN",
		}
	}
	return check{Label: "Editor", Status: "OK", Detail: fmt.Sprintf("%s (via %s)", editor, source)}
}

// --- color helpers (respect NO_COLOR) ---

var doctorUseColor = os.Getenv("NO_COLOR") == ""

func statusLabel(status string) string {
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

func greenText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[32m" + s + "\x1b[0m"
}

func yellowText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[33m" + s + "\x1b[0m"
}

func dimText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
