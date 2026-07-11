package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifest_parsesValidFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "fs.json")
	os.WriteFile(path, []byte(`{
		"command": "npx",
		"args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
		"env": {"NODE_ENV": "production"}
	}`), 0o644)

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest() error: %v", err)
	}
	if m.Command != "npx" {
		t.Errorf("Command = %q, want npx", m.Command)
	}
	if len(m.Args) != 3 {
		t.Errorf("Args len = %d, want 3", len(m.Args))
	}
	if m.Env["NODE_ENV"] != "production" {
		t.Errorf("Env[NODE_ENV] = %q", m.Env["NODE_ENV"])
	}
}

func TestLoadManifest_errorsOnMissingCommand(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.json")
	os.WriteFile(path, []byte(`{"args": ["foo"]}`), 0o644)

	_, err := LoadManifest(path)
	if err == nil {
		t.Error("expected error for missing command")
	}
}

func TestDiscoverManifests_findsJsonFiles(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "alpha.json"), []byte(`{"command":"echo"}`), 0o644)
	os.WriteFile(filepath.Join(tmp, "beta.json"), []byte(`{"command":"cat"}`), 0o644)
	os.WriteFile(filepath.Join(tmp, "readme.txt"), []byte(`not a manifest`), 0o644)

	manifests := DiscoverManifests([]string{tmp})
	if len(manifests) != 2 {
		t.Fatalf("expected 2 manifests, got %d", len(manifests))
	}
	if manifests["alpha"] == nil || manifests["alpha"].Command != "echo" {
		t.Errorf("alpha manifest missing or wrong")
	}
	if manifests["beta"] == nil || manifests["beta"].Command != "cat" {
		t.Errorf("beta manifest missing or wrong")
	}
}

func TestDiscoverManifests_skipsMissingDirs(t *testing.T) {
	manifests := DiscoverManifests([]string{"/nonexistent"})
	if len(manifests) != 0 {
		t.Errorf("expected 0 manifests for missing dir, got %d", len(manifests))
	}
}
