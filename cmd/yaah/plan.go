package yaah

import (
	"os"
	"path/filepath"

	"github.com/buchenberg/yaah/internal/config"
)

// planSearchPaths returns the directories to scan for plans, in order.
func planSearchPaths() []string {
	home := config.HomeDir()

	// 1. Project-level (walk up from cwd)
	cwd, _ := os.Getwd()
	var projectDirs []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		projectDirs = append(projectDirs, filepath.Join(dir, ".agents", "plans"))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	// 2. User-level cross-tool
	userDir := filepath.Join(home, ".agents", "plans")

	var dirs []string
	dirs = append(dirs, projectDirs...)
	dirs = append(dirs, userDir)
	return dirs
}
