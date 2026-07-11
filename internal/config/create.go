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
  openai:
    base_url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
  ollama:
    base_url: http://localhost:11434/v1
    api_key: ollama

default:
  # Change this to the model you want. Use provider/model syntax (e.g. openai/gpt-4o).
  # The provider prefix is stripped before sending to the API.
  model: openai/gpt-4o-mini
  small_model: openai/gpt-4o-mini
  max_iterations: 50
  approval: ask                          # ask | allow | deny

log_level: INFO
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
