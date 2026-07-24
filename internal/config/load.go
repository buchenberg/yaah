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

// Defaults hold the default agent model and loop settings.
type Defaults struct {
	Provider              string  `yaml:"provider"`
	Model                 string  `yaml:"model"`
	SmallModel            string  `yaml:"small_model"`
	MaxIterations         int     `yaml:"max_iterations"`
	MaxTurns              int     `yaml:"max_turns"` // soft cap on tool-using turns; 0 = off
	ContextWindow         int     `yaml:"context_window"`
	Approval              string  `yaml:"approval"`
	MaxInlineToolsPerTurn int     `yaml:"max_inline_tools_per_turn"` // 0 = unlimited
	EstimateFactor        float64 `yaml:"estimate_factor"`           // 0 = default (1.3)

	// Compaction controls context summarisation behaviour.
	CompactionThreshold    float64 `yaml:"compaction_threshold"`     // fraction of ContextWindow; 0 = 0.5
	RawCompactionThreshold float64 `yaml:"raw_compaction_threshold"` // fraction ignoring cache; 0 = 0.5

	// Loop detection governs when the agent halts on repeated tool calls.
	LoopDetectCount  int `yaml:"loop_detect_count"`  // identical calls to trigger halt; 0 = default (5)
	LoopDetectWindow int `yaml:"loop_detect_window"` // sliding window size; 0 = default (10)

	// Provider resilience: retry on transient errors with backoff.
	MaxRetries       int `yaml:"max_retries"`        // 0 = no retries (default)
	RetryBackoffSecs int `yaml:"retry_backoff_secs"` // seconds; 0 = default (1)

	// Concurrency and caching toggles.
	MaxToolConcurrency int  `yaml:"max_tool_concurrency"`    // concurrent tool goroutines; 0 = unlimited
	PromptCaching      bool `yaml:"prompt_caching"`          // inject Anthropic cache-control breakpoints
	ReasoningProtect   int  `yaml:"reasoning_protect_turns"` // preserve reasoning in recent N turns; 0 = default (2)
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

// FallbackConfig configures the fallback provider/model used when the
// primary provider returns auth, billing, or rate-limit errors.
type FallbackConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// AgentConfig holds all agent-related configuration.
type AgentConfig struct {
	Default    Defaults         `yaml:"default"`
	Fallback   FallbackConfig   `yaml:"fallback"`
	SubAgent   SubAgentConfig   `yaml:"subagent"`
	Middleware MiddlewareConfig `yaml:"middleware"`
}

// SubAgentConfig configures the task tool's sub-agent lifecycle:
// nesting depth, concurrency, default timeout, provider/model, and
// per-role overrides. When provider and model are unset, sub-agents
// inherit the planner's provider and model.
type SubAgentConfig struct {
	// Provider selects which provider sub-agents use (default: main provider).
	Provider string `yaml:"provider"`

	// Model selects which model sub-agents use (default: main model).
	Model string `yaml:"model"`

	// MaxConcurrency caps simultaneous task tool calls per iteration.
	// 0 means unlimited.
	MaxConcurrency int `yaml:"max_concurrency"`

	// DefaultTimeout is applied when a task call supplies no timeout
	// and the role profile has none. Seconds. 0 means no timeout.
	DefaultTimeout int `yaml:"default_timeout"`

	// StuckChildTimeout is the duration without a heartbeat before a
	// sub-agent is declared stuck and force-cancelled. The timer resets
	// on every iteration (heartbeat), so this is a per-iteration liveness
	// guard, not a total budget. Seconds. 0 disables.
	StuckChildTimeout int `yaml:"stuck_child_timeout"`

	// DefaultMaxTurns is the fallback soft turn cap when no role-specific
	// override is set. 0 means unlimited (off).
	DefaultMaxTurns int `yaml:"default_max_turns"`

	// JSONMode enables structured output via response_format json_object.
	// Individual roles may override with their own json_mode setting.
	JSONMode bool `yaml:"json_mode"`

	// OutputLimit caps the final synthesized result from a sub-agent in
	// bytes before it reaches the orchestrator. 0 means unlimited.
	OutputLimit int `yaml:"output_limit"`

	// Roles holds per-role overrides keyed by role name
	// ("analyst", "developer", "tester", "reviewer").
	Roles map[string]RoleConfig `yaml:"roles"`
}

// RoleConfig overrides a single role's default timeout, iteration cap,
// turn cap, provider, model, concurrency, and output format.
type RoleConfig struct {
	Timeout           int    `yaml:"timeout"`             // seconds; 0 = use role default
	MaxIterations     int    `yaml:"max_iterations"`      // 0 = use role default
	MaxTurns          int    `yaml:"max_turns"`           // soft turn cap; 0 = use role default
	JSONMode          bool   `yaml:"json_mode"`           // structured output toggle
	ContextWindow     int    `yaml:"context_window"`      // 0 = inherit halved parent default
	OutputLimit       int    `yaml:"output_limit"`        // bytes; 0 = use config default
	Provider          string `yaml:"provider"`            // per-role provider override; "" = inherit
	Model             string `yaml:"model"`               // per-role model override; "" = inherit
	MaxConcurrency    int    `yaml:"max_concurrency"`     // per-role max sub-agent spawns; 0 = use config default
	StuckChildTimeout int    `yaml:"stuck_child_timeout"` // seconds; 0 = use global default
}

// Config is the full yaah configuration loaded from ~/.yaah/config.yaml.
type Config struct {
	Providers     map[string]Provider `yaml:"providers"`
	Agent         AgentConfig         `yaml:"agents"`
	Hooks         Hooks               `yaml:"hooks"`
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
	// Verbose enables detailed span attributes/events: full model content,
	// reasoning, tool-call arguments, and conversation context. Off by
	// default to keep Jaeger payloads light; turn on when diagnosing
	// agent-loop behaviour. Only effective when Enabled is true.
	Verbose bool `yaml:"verbose"`
}

// defaultConfig returns the built-in defaults used when no config file exists.
func defaultConfig() *Config {
	return &Config{
		Agent: AgentConfig{
			Default: Defaults{
				Model:                  "deepseek/deepseek-v4-pro",
				SmallModel:             "deepseek/deepseek-v4-flash",
				MaxIterations:          50,
				ContextWindow:          128000,
				Approval:               "ask",
				CompactionThreshold:    0.5,
				RawCompactionThreshold: 0.5,
				LoopDetectCount:        5,
				LoopDetectWindow:       10,
				RetryBackoffSecs:       1,
				ReasoningProtect:       2,
			},
			SubAgent: SubAgentConfig{
				MaxConcurrency:    3,
				StuckChildTimeout: 60,
				OutputLimit:       51200,
			},
		},
		Observability: ObservabilityConfig{
			Otel: OtelConfig{
				Enabled:     false,
				Endpoint:    "localhost:4317",
				ServiceName: "yaah",
				Traces:      true,
				Metrics:     false,
				Verbose:     false,
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

// HasOldConfig checks a raw config file for old-style top-level "default:"
// or singular "agent:" keys that need migration to "agents:".
func HasOldConfig(data []byte) bool {
	s := string(data)
	return strings.Contains(s, "\ndefault:") || strings.Contains(s, "\nagent:")
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
