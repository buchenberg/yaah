package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Provider holds connection details for a model provider.
type Provider struct {
	BaseURL        string   `yaml:"base_url"`
	APIKey         string   `yaml:"api_key"`
	Name           string   `yaml:"name,omitempty"`
	Models         []string `yaml:"models,omitempty"`
	TimeoutSeconds int      `yaml:"timeout,omitempty"`
}

// Defaults hold the default model and agent loop settings.
type Defaults struct {
	Provider      string `yaml:"provider"`
	Model         string `yaml:"model"`
	SmallModel    string `yaml:"small_model"`
	MaxIterations int    `yaml:"max_iterations"`
	ContextWindow int    `yaml:"context_window"`
	Approval      string `yaml:"approval"`

	// FallbackProvider is the provider name to use when the primary
	// provider returns auth, billing, or rate-limit errors.
	FallbackProvider string `yaml:"fallback_provider"`

	// FallbackModel is the model name to use with FallbackProvider.
	// When empty, the default model is used.
	FallbackModel string `yaml:"fallback_model"`
}

// Hooks holds configuration for external integrations via JSONL hook events.
type Hooks struct {
	Dir string `yaml:"dir"` // directory for JSONL hook event files
}

// MiddlewareConfig controls which middleware runs in the agent pipeline.
type MiddlewareConfig struct {
	Enabled  []string `yaml:"enabled"`
	Disabled []string `yaml:"disabled"`
}

// AgentConfig holds agent loop and middleware pipeline settings.
type AgentConfig struct {
	Middleware MiddlewareConfig `yaml:"middleware"`
	SubAgent   SubAgentConfig   `yaml:"subagent"`
	Executor   ExecutorConfig   `yaml:"executor"`
}

// ExecutorConfig configures the inner executor loop used by the dual-loop
// architecture. When unset, the inner loop uses the main provider and model.
// The model field accepts provider/model syntax (e.g. "deepseek/deepseek-v4-flash").
type ExecutorConfig struct {
	Provider      string `yaml:"provider"`       // provider name (default: main provider)
	Model         string `yaml:"model"`          // model name, accepts "provider/model" prefix
	MaxIterations int    `yaml:"max_iterations"` // max inner rounds per outer turn (default: 10)
}

// SubAgentConfig configures the task tool's sub-agent lifecycle:
// nesting depth, concurrency, default timeout, and per-role overrides.
type SubAgentConfig struct {
	// MaxDepth is the global default for how many task calls a single
	// Loop may issue. 0 means unlimited.
	MaxDepth int `yaml:"max_depth"`

	// MaxConcurrency caps simultaneous task tool calls per iteration.
	// 0 means unlimited.
	MaxConcurrency int `yaml:"max_concurrency"`

	// DefaultTimeout is applied when a task call supplies no timeout
	// and the role profile has none. Seconds. 0 means no timeout.
	DefaultTimeout int `yaml:"default_timeout"`

	// Roles holds per-role overrides keyed by role name
	// ("worker", "reviewer", "planner").
	Roles map[string]RoleConfig `yaml:"roles"`
}

// RoleConfig overrides a single role's default timeout and iteration cap.
type RoleConfig struct {
	Timeout       int `yaml:"timeout"`        // seconds; 0 = use role default
	MaxIterations int `yaml:"max_iterations"` // 0 = use role default
	MaxDepth      int `yaml:"max_depth"`      // 0 = use global MaxDepth
}

// Config is the full yaah configuration loaded from ~/.yaah/config.yaml.
type Config struct {
	Providers     map[string]Provider `yaml:"providers"`
	Default       Defaults            `yaml:"default"`
	Hooks         Hooks               `yaml:"hooks"`
	Agent         AgentConfig         `yaml:"agent"`
	Editor        string              `yaml:"editor"`
	Observability ObservabilityConfig `yaml:"observability"`
}

// ObservabilityConfig holds OpenTelemetry tracing and metrics settings.
type ObservabilityConfig struct {
	Otel OtelConfig `yaml:"otel"`
}

// OtelConfig controls the OpenTelemetry OTLP exporter.
type OtelConfig struct {
	Enabled     bool   `yaml:"enabled"`      // must be true to activate
	Endpoint    string `yaml:"endpoint"`     // OTLP gRPC endpoint (e.g. "localhost:4317")
	ServiceName string `yaml:"service_name"` // displayed in the tracing UI
	Traces      bool   `yaml:"traces"`       // enable trace spans
	Metrics     bool   `yaml:"metrics"`      // enable OTLP metrics
}

// defaultConfig returns the built-in defaults used when no config file exists.
func defaultConfig() *Config {
	return &Config{
		Default: Defaults{
			Model:         "deepseek/deepseek-v4-pro",
			SmallModel:    "deepseek/deepseek-v4-flash",
			MaxIterations: 50,
			ContextWindow: 128000,
			Approval:      "ask",
		},
		Agent: AgentConfig{
			SubAgent: SubAgentConfig{
				MaxDepth:       3,
				MaxConcurrency: 3,
			},
			Executor: ExecutorConfig{
				MaxIterations: 10,
			},
		},
		Observability: ObservabilityConfig{
			Otel: OtelConfig{
				Enabled:     false,
				Endpoint:    "localhost:4317",
				ServiceName: "yaah",
				Traces:      true,
				Metrics:     false,
			},
		},
	}
}

// envVarRe matches ${VAR_NAME} patterns for env substitution.
var envVarRe = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// substituteEnvVars replaces ${VAR} patterns with the corresponding env var.
func substituteEnvVars(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1] // strip ${ and }
		return os.Getenv(varName)
	})
}

// Load reads the config file from ConfigPath(). If the file doesn't exist,
// it returns a Config populated with built-in defaults. Environment variable
// references in the form ${VAR_NAME} are substituted after parsing.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultConfig(), nil
		}
		return nil, fmt.Errorf("cannot read config %s: %w", path, err)
	}

	cfg := defaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("cannot parse config %s: %w", path, err)
	}

	// Substitute env vars in provider fields
	for name, p := range cfg.Providers {
		p.APIKey = substituteEnvVars(p.APIKey)
		p.BaseURL = substituteEnvVars(p.BaseURL)
		cfg.Providers[name] = p
	}

	if cfg.Hooks.Dir != "" {
		cfg.Hooks.Dir = expandHomeDir(cfg.Hooks.Dir)
	}

	return cfg, nil
}

// ResolveEditor returns the editor command to use, with this priority:
//  1. cfg.Editor (config file)
//  2. $EDITOR environment variable
//  3. $VISUAL environment variable
//  4. "vi" (hardcoded fallback)
//
// If cfg is nil, only environment variables and the fallback are checked.
func ResolveEditor(cfg *Config) string {
	if cfg != nil && cfg.Editor != "" {
		return cfg.Editor
	}
	if editor := os.Getenv("EDITOR"); editor != "" {
		return editor
	}
	if visual := os.Getenv("VISUAL"); visual != "" {
		return visual
	}
	return "vi"
}

func expandHomeDir(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	if strings.HasPrefix(path, "$HOME/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		return filepath.Join(home, path[6:])
	}
	return path
}
