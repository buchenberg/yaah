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
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	Name    string   `yaml:"name,omitempty"`
	Models  []string `yaml:"models,omitempty"`
}

// Defaults hold the default model and agent loop settings.
type Defaults struct {
	Provider      string `yaml:"provider"`
	Model         string `yaml:"model"`
	SmallModel    string `yaml:"small_model"`
	MaxIterations int    `yaml:"max_iterations"`
	ContextWindow int    `yaml:"context_window"`
	Approval      string `yaml:"approval"`
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
}

// Config is the full yaah configuration loaded from ~/.yaah/config.yaml.
type Config struct {
	Providers map[string]Provider `yaml:"providers"`
	Default   Defaults            `yaml:"default"`
	Hooks     Hooks               `yaml:"hooks"`
	Agent     AgentConfig         `yaml:"agent"`
	LogLevel  string              `yaml:"log_level"`
}

// defaultConfig returns the built-in defaults used when no config file exists.
func defaultConfig() *Config {
	return &Config{
		Default: Defaults{
			Model:         "openai/gpt-4o-mini",
			SmallModel:    "openai/gpt-4o-mini",
			MaxIterations: 50,
			ContextWindow: 128000,
			Approval:      "ask",
		},
		LogLevel: "INFO",
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
