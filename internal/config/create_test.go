package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateDefault_writesConfigFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	err := CreateDefault()
	if err != nil {
		t.Fatalf("CreateDefault() error: %v", err)
	}

	// File should exist
	path := filepath.Join(tmp, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// Should contain expected default values
	content := string(data)
	for _, want := range []string{
		"provider: deepseek",
		"model: deepseek-v4-pro",
		"max_iterations: 50",
		"approval: ask",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config file missing %q\ngot:\n%s", want, content)
		}
	}
}

func TestCreateDefault_idempotentDoesNotOverwrite(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YAAH_HOME", tmp)

	// Create a custom config first
	custom := `
default:
  model: ollama/llama3
`
	path := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// CreateDefault should NOT overwrite
	err := CreateDefault()
	if err != nil {
		t.Fatalf("CreateDefault() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "ollama/llama3") {
		t.Errorf("CreateDefault overwrote existing config\ngot:\n%s", string(data))
	}
}
