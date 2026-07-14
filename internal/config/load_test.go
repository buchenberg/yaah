package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_returnsDefaultsWhenFileMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Defaults from the plan §3.2
	if cfg.Default.Model != "openai/gpt-4o-mini" {
		t.Errorf("Default.Model = %q, want %q", cfg.Default.Model, "openai/gpt-4o-mini")
	}
	if cfg.Default.SmallModel != "openai/gpt-4o-mini" {
		t.Errorf("Default.SmallModel = %q, want %q", cfg.Default.SmallModel, "openai/gpt-4o-mini")
	}
	if cfg.Default.MaxIterations != 50 {
		t.Errorf("Default.MaxIterations = %d, want 50", cfg.Default.MaxIterations)
	}
	if cfg.Default.Approval != "ask" {
		t.Errorf("Default.Approval = %q, want %q", cfg.Default.Approval, "ask")
	}
}

func TestLoad_parsesYAMLConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	// Write a config with custom values
	configContent := `
providers:
  openai:
    base_url: https://api.openai.com/v1
    api_key: sk-test123
  ollama:
    base_url: http://localhost:11434/v1
    api_key: ollama

default:
  model: openai/gpt-4o
  small_model: openai/gpt-4o-mini
  max_iterations: 25
  approval: allow

log_level: DEBUG
`
	err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configContent), 0o644)
	if err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Default.Model != "openai/gpt-4o" {
		t.Errorf("Default.Model = %q, want %q", cfg.Default.Model, "openai/gpt-4o")
	}
	if cfg.Default.MaxIterations != 25 {
		t.Errorf("Default.MaxIterations = %d, want 25", cfg.Default.MaxIterations)
	}
	if cfg.Default.Approval != "allow" {
		t.Errorf("Default.Approval = %q, want %q", cfg.Default.Approval, "allow")
	}

	// Check providers
	if len(cfg.Providers) != 2 {
		t.Fatalf("Providers count = %d, want 2", len(cfg.Providers))
	}
	if cfg.Providers["openai"].APIKey != "sk-test123" {
		t.Errorf("Providers[openai].APIKey = %q, want %q", cfg.Providers["openai"].APIKey, "sk-test123")
	}
	if cfg.Providers["ollama"].BaseURL != "http://localhost:11434/v1" {
		t.Errorf("Providers[ollama].BaseURL = %q, want %q", cfg.Providers["ollama"].BaseURL, "http://localhost:11434/v1")
	}

	if cfg.LogLevel != "DEBUG" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "DEBUG")
	}
}

func TestLoad_envVarSubstitution(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)
	t.Setenv("OPENAI_API_KEY", "sk-from-env-456")

	configContent := `
providers:
  openai:
    base_url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
`
	err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configContent), 0o644)
	if err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Providers["openai"].APIKey != "sk-from-env-456" {
		t.Errorf("Providers[openai].APIKey = %q, want %q (env substitution failed)",
			cfg.Providers["openai"].APIKey, "sk-from-env-456")
	}
}

func TestLoad_providerModelsOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	configContent := `
providers:
  openai:
    base_url: https://api.openai.com/v1
    api_key: sk-test
    models:
      - gpt-4o
      - gpt-4o-mini
      - o1
  ollama:
    base_url: http://localhost:11434/v1
    api_key: ollama

default:
  model: openai/gpt-4o-mini
  small_model: openai/gpt-4o-mini
  max_iterations: 50
  approval: ask
`
	err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configContent), 0o644)
	if err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	openaiModels := cfg.Providers["openai"].Models
	if len(openaiModels) != 3 {
		t.Fatalf("expected 3 openai models, got %d: %v", len(openaiModels), openaiModels)
	}
	if openaiModels[0] != "gpt-4o" || openaiModels[1] != "gpt-4o-mini" || openaiModels[2] != "o1" {
		t.Errorf("openai models = %v, want [gpt-4o gpt-4o-mini o1]", openaiModels)
	}

	// Provider without models should have nil/empty slice
	ollamaModels := cfg.Providers["ollama"].Models
	if len(ollamaModels) != 0 {
		t.Errorf("ollama models should be empty, got %v", ollamaModels)
	}
}
