package substrate

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHomeDir replaces a leading ~/ or ~ with the user's home directory.
func ExpandHomeDir(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		return home
	}
	return path
}

// ResolvePathWithPV resolves a user-supplied path through the PathValidator
// when one is configured, falling back to ExpandHomeDir (legacy behaviour).
// File-accessing tools should call this during Execute instead of
// ExpandHomeDir directly.
func ResolvePathWithPV(pv *PathValidator, input string) (string, error) {
	if pv != nil {
		return pv.ResolvePath(input)
	}
	if input == "" {
		return ".", nil
	}
	return ExpandHomeDir(input), nil
}

// ResolveWritePathWithPV is ResolvePathWithPV plus the harness-configuration
// guard when a validator is attached. A nil validator keeps the legacy
// behaviour: there is no workspace to protect, so the path is only home-expanded
// and the workspace write methods do the guarding when they can.
func ResolveWritePathWithPV(pv *PathValidator, input string) (string, error) {
	if pv != nil {
		return pv.ResolveWritePath(input)
	}
	if input == "" {
		return ".", nil
	}
	return ExpandHomeDir(input), nil
}
