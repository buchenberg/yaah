package yaah

import (
	"os"
	"path/filepath"
)

// planSearchPaths returns the directories to scan for plans, in order.
func planSearchPaths() []string {
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

	// 2. User-level cross-tool — the REAL home dir, not config.HomeDir()
	// (which is ~/.yaah). Plans live at ~/.agents/plans per the
	// cross-tool convention, mirroring skillSearchPaths (review B5).
	userHome, _ := os.UserHomeDir()
	userDir := filepath.Join(userHome, ".agents", "plans")

	var dirs []string
	dirs = append(dirs, projectDirs...)
	dirs = append(dirs, userDir)
	return dirs
}
