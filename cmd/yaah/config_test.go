package yaah

import (
	"bytes"
	"os"
	"path/filepath"
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
	if !strings.Contains(output, "deepseek-v4-pro") {
		t.Errorf("config show output missing default model\ngot:\n%s", output)
	}
	// Should NOT print raw API keys
	if strings.Contains(output, "sk-") {
		t.Errorf("config show leaked API key in output\ngot:\n%s", output)
	}
}

// TestConfigShow_printsEmbeddingSection pins the #316 regression: the
// top-level embedding: stanza must render as an [embedding] section,
// both when configured and when absent.
func TestConfigShow_printsEmbeddingSection(t *testing.T) {
	runConfigShow := func(t *testing.T, configYAML string) string {
		t.Helper()
		tmp := t.TempDir()
		t.Setenv("YAAH_HOME", tmp)
		if configYAML != "" {
			if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configYAML), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)
		defer rootCmd.ResetFlags()

		rootCmd.SetArgs([]string{"config", "show"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Execute() error: %v", err)
		}
		return buf.String()
	}

	t.Run("configured", func(t *testing.T) {
		output := runConfigShow(t, "embedding:\n  provider: deepseek\n  model: text-embedding-v1\n")
		for _, want := range []string{"[embedding]", "provider: deepseek", "model:    text-embedding-v1"} {
			if !strings.Contains(output, want) {
				t.Errorf("config show output missing %q\ngot:\n%s", want, output)
			}
		}
	})

	t.Run("not configured", func(t *testing.T) {
		output := runConfigShow(t, "")
		if !strings.Contains(output, "[embedding]") {
			t.Errorf("config show output missing [embedding] section\ngot:\n%s", output)
		}
		if !strings.Contains(output, "(not configured") {
			t.Errorf("unconfigured embedding should say so\ngot:\n%s", output)
		}
	})
}
