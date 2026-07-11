// Package config loads and manages the yaah configuration file at
// ~/.yaah/config.yaml (or $YAAH_HOME/config.yaml if the env var is set).
//
// The config is intentionally minimal: providers, default model settings,
// and a few display preferences. Everything else lives in ~/.agents/ or
// the project's .agents/ directory.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigPath returns the path to the yaah config file.
//
// Resolution order:
//  1. $YAAH_HOME/config.yaml (if YAAH_HOME is set and non-empty)
//  2. $HOME/.yaah/config.yaml (default)
func ConfigPath() (string, error) {
	if home := os.Getenv("YAAH_HOME"); home != "" {
		return filepath.Join(home, "config.yaml"), nil
	}

	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	return filepath.Join(userHome, ".yaah", "config.yaml"), nil
}
