package repl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendHistory_writesLineToFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	err := AppendHistory("hello world")
	if err != nil {
		t.Fatalf("AppendHistory() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "history"))
	if err != nil {
		t.Fatalf("history file not created: %v", err)
	}

	want := "hello world\n"
	if string(data) != want {
		t.Errorf("history = %q, want %q", string(data), want)
	}
}

func TestAppendHistory_appendsMultipleLines(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	for _, line := range []string{"first", "second", "third"} {
		if err := AppendHistory(line); err != nil {
			t.Fatalf("AppendHistory(%q): %v", line, err)
		}
	}

	data, _ := os.ReadFile(filepath.Join(tmp, "history"))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "first" || lines[1] != "second" || lines[2] != "third" {
		t.Errorf("lines = %v", lines)
	}
}

func TestAppendHistory_rotatesWhenTooLarge(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	// Write enough lines to exceed the rotation threshold.
	// We set a tiny max for testing via the package-internal variable.
	oldMax := maxHistoryBytes
	maxHistoryBytes = 200 // 200 bytes — very small
	defer func() { maxHistoryBytes = oldMax }()

	for i := 0; i < 50; i++ {
		if err := AppendHistory(strings.Repeat("x", 30)); err != nil {
			t.Fatalf("AppendHistory() iteration %d: %v", i, err)
		}
	}

	data, _ := os.ReadFile(filepath.Join(tmp, "history"))
	// After rotation, file should be smaller than 2x the max
	if len(data) > maxHistoryBytes*2 {
		t.Errorf("history file too large after rotation: %d bytes (max %d)", len(data), maxHistoryBytes)
	}
	// File should still have content
	if len(data) == 0 {
		t.Error("history file empty after rotation")
	}
}
