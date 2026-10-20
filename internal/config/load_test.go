package config

import (
	"bytes"
	"io"
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
	if cfg.Agent.Default.Model != "deepseek/deepseek-v4-pro" {
		t.Errorf("Default.Model = %q, want %q", cfg.Agent.Default.Model, "deepseek/deepseek-v4-pro")
	}
	if cfg.Agent.Default.SmallModel != "deepseek/deepseek-v4-flash" {
		t.Errorf("Default.SmallModel = %q, want %q", cfg.Agent.Default.SmallModel, "deepseek/deepseek-v4-flash")
	}
	if cfg.Agent.Default.MaxLoopCycles != 50 {
		t.Errorf("Default.MaxLoopCycles = %d, want 50", cfg.Agent.Default.MaxLoopCycles)
	}
	if cfg.Agent.Default.Approval != "ask" {
		t.Errorf("Default.Approval = %q, want %q", cfg.Agent.Default.Approval, "ask")
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

agents:
  default:
    model: openai/gpt-4o
    small_model: openai/gpt-4o-mini
    max_iterations: 25
    approval: allow
`
	err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configContent), 0o644)
	if err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Agent.Default.Model != "openai/gpt-4o" {
		t.Errorf("Default.Model = %q, want %q", cfg.Agent.Default.Model, "openai/gpt-4o")
	}
	if cfg.Agent.Default.MaxLoopCycles != 25 {
		t.Errorf("Default.MaxLoopCycles = %d, want 25", cfg.Agent.Default.MaxLoopCycles)
	}
	if cfg.Agent.Default.Approval != "allow" {
		t.Errorf("Default.Approval = %q, want %q", cfg.Agent.Default.Approval, "allow")
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

	if cfg.Observability.Otel.Enabled {
		t.Error("Observability.Otel.Enabled should default to false")
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

	// Load preserves raw ${VAR} references — Resolve() substitutes them.
	if cfg.Providers["openai"].APIKey != "${OPENAI_API_KEY}" {
		t.Errorf("Providers[openai].APIKey = %q, want %q (raw ${VAR} not preserved)",
			cfg.Providers["openai"].APIKey, "${OPENAI_API_KEY}")
	}

	resolved := Resolve(cfg.Providers["openai"])
	if resolved.APIKey != "sk-from-env-456" {
		t.Errorf("Resolve().APIKey = %q, want %q (env substitution failed)",
			resolved.APIKey, "sk-from-env-456")
	}
}

func TestResolve_oauthScopeEnvSubstitution(t *testing.T) {
	t.Setenv("YAAH_TEST_OAUTH_SCOPE", "openid profile")

	got := Resolve(Provider{
		Auth:       "oauth",
		OAuthScope: "${YAAH_TEST_OAUTH_SCOPE}",
	}).OAuthScope

	if got != "openid profile" {
		t.Errorf("Resolve().OAuthScope = %q, want %q (scope not env-substituted)", got, "openid profile")
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

agents:
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

	openaiModels := cfg.Providers["openai"].ModelNames()
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

func TestLoad_shepherdTracePathExpandsTilde(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	configContent := `
agents:
  default:
    shepherd_trace_path: ~/custom-traces
    model: test/model
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, "custom-traces")
	if cfg.Agent.Default.ShepherdTracePath != expected {
		t.Errorf("ShepherdTracePath = %q, want %q (tilde expanded)", cfg.Agent.Default.ShepherdTracePath, expected)
	}
}

func TestLoad_shepherdTracePathDefaultsToHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// The raw field stays empty so TracePath() can apply the default.
	if cfg.Agent.Default.ShepherdTracePath != "" {
		t.Errorf("ShepherdTracePath should be empty by default, got %q", cfg.Agent.Default.ShepherdTracePath)
	}
	if got, want := cfg.Agent.Default.TracePath(), filepath.Join(tmp, "trace.db"); got != want {
		t.Errorf("TracePath() = %q, want %q", got, want)
	}
}

func TestLoad_roleTurnCheckpoints(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	configContent := `
agents:
  default:
    model: test/model
  subagent:
    roles:
      developer:
        turn_checkpoints: true
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	dev := cfg.Agent.SubAgent.Roles["developer"]
	if !dev.TurnCheckpoints {
		t.Error("developer.TurnCheckpoints = false, want true")
	}

	// A role with no checkpoint key defaults it to false.
	other := cfg.Agent.SubAgent.Roles["unset-role"]
	if other.TurnCheckpoints {
		t.Error("unset role TurnCheckpoints = true, want false")
	}
}

func TestLoad_supervisorHaltRoot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	// Off by default: root halt is a big hammer (adoption plan A3's
	// gate), so an absent key must read false.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() (defaults) error: %v", err)
	}
	if cfg.Agent.Default.SupervisorHaltRoot {
		t.Error("supervisor_halt_root must default to false")
	}

	configContent := `
agents:
  default:
    supervisor_halt_root: true
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.Agent.Default.SupervisorHaltRoot {
		t.Error("supervisor_halt_root: true must parse into SupervisorHaltRoot")
	}
}

// TestLoad_removedSupervisedKeysAreSilentlyDead pins the end of the
// supervised_task deprecation window: its keys and the per-role
// `supervised` flag are gone, load without warnings, and behave like any
// unknown key.
func TestLoad_removedSupervisedKeysAreSilentlyDead(t *testing.T) {
	content := `
agents:
  default:
    model: test/model
    supervised_worktree: true
    supervised_max_retries: 2
    supervised_repo_path: /tmp/repo
    supervised_worktree_root: /tmp/wt
    supervised_worktree_bootstrap: npm ci
  subagent:
    roles:
      developer:
        supervised: true
`
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w
	_, loadErr := Load()
	os.Stderr = prev
	_ = w.Close()
	data, _ := io.ReadAll(r)
	_ = r.Close()

	if loadErr != nil {
		t.Fatalf("Load() error: %v", loadErr)
	}
	if bytes.Contains(data, []byte("supervised")) {
		t.Errorf("removed supervised keys must die silently, got warnings %q", string(data))
	}
}
