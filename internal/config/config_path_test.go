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
	t.Setenv("HOME", "/tmp/fake-user-home")

	got, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath() error: %v", err)
	}

	want := filepath.Join("/tmp/fake-user-home", ".yaah", "config.yaml")
	if got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}
