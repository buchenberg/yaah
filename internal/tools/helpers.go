package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Shared constants used across multiple tools.

const toolResultMaxLen = 8192
const bashDefaultTimeout = 30 * time.Second
const bashMaxOutput = 1 << 20 // 1 MiB

// expandHomeDir replaces a leading ~/ or ~ with the user's home directory.
func expandHomeDir(path string) string {
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

// truncateOutput caps a command's output to bashMaxOutput with a truncation marker.
func truncateOutput(b []byte) []byte {
	if len(b) <= bashMaxOutput {
		return b
	}
	return append(b[:bashMaxOutput], []byte("\n...[output truncated]...")...)
}

// rgAvailable checks if ripgrep is on PATH.
func rgAvailable() bool {
	_, err := exec.LookPath("rg")
	return err == nil
}

// commonIgnoreDirs lists directories that are nearly always safe to skip
// during filesystem walks (glob, grep, ls).
var commonIgnoreDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".mypy_cache": true, ".pytest_cache": true,
	".tox": true, ".eggs": true, "dist": true, "build": true,
	".next": true, ".nuxt": true, ".output": true,
	"vendor": true, ".idea": true, ".vscode": true,
	"target": true, ".dart_tool": true, ".turbo": true,
	"bin": true, "obj": true,
}

// binaryExtensions is a rough set of extensions that should be skipped
// during content searches.
var binaryExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
	".pdf": true, ".zip": true, ".gz": true, ".tar": true, ".bz2": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".class": true, ".pyc": true, ".pyo": true, ".o": true, ".obj": true,
	".db": true, ".sqlite": true, ".sqlite3": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true,
	".min.js": true, ".min.css": true, ".map": true,
}

// dangerousCommands is a coarse deny-list of obviously destructive shell
// patterns. It is NOT a security boundary: model-generated shell can trivially
// evade a substring deny-list (whitespace, shell variables, indirection,
// decoders). Real protection comes from the approval gate (config.Approval);
// this list only catches the most blatant mistakes.
var dangerousCommands = []string{
	"rm -rf /", "rm -rf ~", "rm -rf .",
	"shutdown", "reboot", "halt",
	"mkfs", "mkswap",
	"dd if=", ":(){ :|:& };:",
	"chmod 777 /", "chown -R",
	"remove-item -recurse -force c:\\",
	"format-volume", "stop-computer", "restart-computer",
	"clear-disk", "initialize-disk",
}

// isDangerous reports whether cmd matches a known destructive pattern. It is a
// best-effort guard only (see dangerousCommands).
func isDangerous(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, dangerous := range dangerousCommands {
		if strings.Contains(lower, dangerous) {
			return true
		}
	}
	return false
}
