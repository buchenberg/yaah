package yaah

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfigShow_printsConfig(t *testing.T) {
	// Use a temp YAAH_HOME with a known config
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	// Patch rootCmd to write to our buffer instead of stdout
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	defer rootCmd.ResetFlags()

	rootCmd.SetArgs([]string{"config", "show"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := buf.String()
	// Should print the default model (from built-in defaults since file is missing)
	if !strings.Contains(output, "deepseek-chat") {
		t.Errorf("config show output missing default model\ngot:\n%s", output)
	}
	// Should NOT print raw API keys
	if strings.Contains(output, "sk-") {
		t.Errorf("config show leaked API key in output\ngot:\n%s", output)
	}
}
