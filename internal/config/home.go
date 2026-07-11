package config

import (
	"os"
	"path/filepath"
)

// HomeDir returns the yaah home directory. Resolution order:
//  1. $YAAH_HOME (if set)
//  2. $HOME/.yaah (default)
func HomeDir() string {
	if h := os.Getenv("YAAH_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".yaah")
}
