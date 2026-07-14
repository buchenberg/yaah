package config

import (
	"fmt"
	"os"
	"regexp"

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

// Config is the full yaah configuration loaded from ~/.yaah/config.yaml.
type Config struct {
	Providers map[string]Provider `yaml:"providers"`
	Default   Defaults            `yaml:"default"`
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

	return cfg, nil
}
