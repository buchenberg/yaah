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
    name: OpenAI                          # display name (optional, shown in /model)
    base_url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    # models:                              # optional: override the API model list
    #   - gpt-4o
    #   - gpt-4o-mini
  ollama:
    name: Ollama
    base_url: http://localhost:11434/v1
    api_key: ollama

default:
  provider: openai                        # which provider to use by default
  model: gpt-4o-mini                      # model name (no provider prefix needed)
  small_model: gpt-4o-mini
  max_iterations: 50
  approval: ask                           # ask | allow | deny

# hooks:
#   dir: ~/.yaah/hooks                    # optional: JSONL event log for external integrations

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
