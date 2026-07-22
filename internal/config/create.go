package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultConfigYAML is the scaffold written to disk on first run.
const defaultConfigYAML = `# yaah configuration — see https://github.com/buchenberg/yaah
# Edit this file to configure providers and defaults.
# Environment variables can be referenced as ${VAR_NAME}.

providers:
  deepseek:
    name: DeepSeek
    base_url: https://api.deepseek.com/v1
    api_key: ${DEEPSEEK_API_KEY}
  ollama:
    name: Ollama
    base_url: http://localhost:11434/v1
    api_key: ollama
    # timeout: 0               # 0 = no timeout (for slow local models)

agents:
  default:
    provider: deepseek
    model: deepseek-v4-pro
    small_model: deepseek-v4-flash
    max_iterations: 50
    approval: ask               # ask | allow | deny
    # max_inline_tools_per_turn: 12  # cap inline tools per turn; 0 = unlimited

  # subagent:
  #   provider: deepseek         # override provider (default: inherit from planner)
  #   model: deepseek-v4-flash   # override model (default: inherit from planner)
  #   max_concurrency: 3
  #   default_timeout: 120

  # fallback:
  #   provider: openrouter        # optional: rotate on auth/billing/rate-limit errors
  #   model: meta-llama/llama-4-maverick

  # middleware:
  #   enabled:
  #     - steer
  #     - followup
  #     - compaction
  #     - approval
  #     - loop_detection
  #   # disabled:
  #   #   - approval

# observability:
#   otel:
#     enabled: true
#     endpoint: localhost:4317
#     service_name: yaah
#     verbose: false

# hooks:
#   dir: ~/.yaah/hooks

# editor: code --wait
`

// CreateDefault writes a scaffold config file to ConfigPath() if and only
// if the file does not already exist. If a config file exists, it is left
// untouched — CreateDefault is idempotent and never overwrites.
func CreateDefault() error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create config directory %s: %w", dir, err)
	}

	// Don't overwrite existing config
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	if err := os.WriteFile(path, []byte(defaultConfigYAML), 0o644); err != nil {
		return fmt.Errorf("cannot write config %s: %w", path, err)
	}

	return nil
}
