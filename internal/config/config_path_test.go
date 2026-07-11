package config

import (
	"path/filepath"
	"testing"
)

func TestConfigPath_resolvesHomeYAAH(t *testing.T) {
	t.Setenv("YAAH_HOME", "/tmp/fake-yaah-home")

	got, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath() error: %v", err)
	}

	want := filepath.Join("/tmp/fake-yaah-home", "config.yaml")
	if got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestConfigPath_noEnvUsesUserHome(t *testing.T) {
	t.Setenv("YAAH_HOME", "")
	// os.UserHomeDir() uses $HOME on Unix and %USERPROFILE% on Windows.
	// We set both to make the test cross-platform.
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	got, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath() error: %v", err)
	}

	want := filepath.Join(homeDir, ".yaah", "config.yaml")
	if got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}
