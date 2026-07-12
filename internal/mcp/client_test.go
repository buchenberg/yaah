package mcp

import (
	"context"
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

func TestLoadManifest_parsesFraming(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "docker.json")
	os.WriteFile(path, []byte(`{
		"command": "docker",
		"args": ["mcp", "gateway", "run"],
		"framing": "newline"
	}`), 0o644)

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest() error: %v", err)
	}
	if m.Framing != "newline" {
		t.Errorf("Framing = %q, want newline", m.Framing)
	}
	// Default transport should still be stdio
	if m.Transport != "stdio" {
		t.Errorf("Transport = %q, want stdio (default)", m.Transport)
	}
}

// TestClient_newlineFraming_handshake — simulates a Docker MCP gateway that
// speaks newline-delimited JSON, not Content-Length. This was the bug that
// caused "invalid character 'C'" warnings and silently dropped the server.
func TestClient_newlineFraming_handshake(t *testing.T) {
	// Build a minimal in-process stdio server: a shell pipeline that
	// responds to "initialize" with a newline-delimited JSON reply, then
	// responds to "tools/list" with an empty tool list.
	// Fake gateway: a shell that reads line-by-line, matches method name,
	// and replies with a newline-delimited JSON object. The discard of
	// stdin via `cat` would race with `read`; instead we use a single
	// `read` loop that handles both consume-and-respond in one pass.
	script := `while IFS= read -r line; do
  case "$line" in
    *initialize*)
      printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"0"}}}'
      ;;
    *tools/list*)
      printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}'
      ;;
  esac
done
`

	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "fake-mcp.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	client := NewClient("fake", Manifest{
		Command:   "/bin/sh",
		Args:      []string{scriptPath},
		Framing:   "newline",
		Transport: "stdio",
	})
	defer client.Close()

	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := len(client.Tools()); got != 0 {
		t.Errorf("Tools() = %d, want 0 (empty server)", got)
	}
}

func TestLoadManifest_defaultsFramingToEmpty(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "fs.json")
	os.WriteFile(path, []byte(`{"command": "npx"}`), 0o644)

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest() error: %v", err)
	}
	if m.Framing != "" {
		t.Errorf("Framing = %q, want empty (default = framed)", m.Framing)
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
