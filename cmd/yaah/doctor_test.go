package yaah

import (
	"bytes"
	"strings"
	"testing"
)

func TestDoctor_reportsStatus(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	defer rootCmd.ResetFlags()

	rootCmd.SetArgs([]string{"doctor"})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := buf.String()

	// Should report on config
	if !strings.Contains(output, "config") {
		t.Errorf("doctor output missing 'config' check\ngot:\n%s", output)
	}

	// Should report on home directory
	if !strings.Contains(strings.ToLower(output), "home") {
		t.Errorf("doctor output missing 'home' check\ngot:\n%s", output)
	}

	// Should have OK or FAIL markers
	if !strings.Contains(output, "OK") && !strings.Contains(output, "WARN") {
		t.Errorf("doctor output missing status indicators\ngot:\n%s", output)
	}
}
