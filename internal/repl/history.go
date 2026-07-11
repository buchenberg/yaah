// Package repl implements the interactive read-eval-print loop for yaah.
//
// In v0.1 the REPL is a plain bufio.Scanner over stdin/stdout with ANSI
// escape codes for color. When the TUI lands in v0.2, the input loop
// swaps to bubbletea but the history and slash-command logic stay.
package repl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxHistoryBytes is the soft cap for the history file. When exceeded,
// the file is trimmed to keep only the most recent entries. Default:
// 10MB per the plan §12.4 decision.
var maxHistoryBytes = 10 * 1024 * 1024

// historyPath returns the path to the history file under YAAH_HOME.
func historyPath() (string, error) {
	home := os.Getenv("YAAH_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		home = filepath.Join(userHome, ".yaah")
	}
	return filepath.Join(home, "history"), nil
}

// AppendHistory appends a single entry to the history file, one per line.
// If the file exceeds maxHistoryBytes after the write, it is rotated:
// only the most recent entries fitting within maxHistoryBytes are kept.
func AppendHistory(entry string) error {
	if entry == "" {
		return nil
	}

	path, err := historyPath()
	if err != nil {
		return err
	}

	// Ensure parent dir exists
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create history directory: %w", err)
	}

	// Read existing content
	existing, _ := os.ReadFile(path)

	// Append the new entry
	line := entry + "\n"
	data := append(existing, []byte(line)...)

	// Rotate if needed
	if len(data) > maxHistoryBytes {
		data = rotateHistory(data, maxHistoryBytes)
	}

	return os.WriteFile(path, data, 0o644)
}

// rotateHistory trims the data to keep only the most recent entries
// that fit within maxBytes. It cuts at a line boundary.
func rotateHistory(data []byte, maxBytes int) []byte {
	if len(data) <= maxBytes {
		return data
	}

	// Start from maxBytes and find the next newline
	start := len(data) - maxBytes
	idx := strings.IndexByte(string(data[start:]), '\n')
	if idx >= 0 {
		start = start + idx + 1
	}

	return data[start:]
}
