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
  glm:
    name: GLM
    base_url: https://api.z.ai/api/paas/v4
    api_key: ${GLM_API_KEY}
  ollama:
    name: Ollama
    base_url: http://localhost:11434/v1
    api_key: ollama

# editor: code --wait                    # editor for 'yaah config edit' (falls back to $EDITOR, $VISUAL, vi)

default:
  provider: deepseek                       # which provider to use by default
  model: deepseek-chat                      # model name (no provider prefix needed)
  small_model: deepseek-chat
  max_iterations: 50
  approval: ask                           # ask | allow | deny

# agent:
#   middleware:
#     enabled:                             # explicit set of middleware to run (in order)
#       - steer
#       - followup
#       - compaction
#       - approval
#       - loop_detection
#     # disabled:                          # exclude specific middleware
#     #   - approval

# hooks:
#   dir: ~/.yaah/hooks                    # optional: JSONL event log for external integrations
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
