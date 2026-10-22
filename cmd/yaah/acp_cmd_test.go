package yaah

import (
	"testing"

	"github.com/buchenberg/yaah/internal/acp"
)

func TestMcpManifestsFromACP(t *testing.T) {
	got := mcpManifestsFromACP([]acp.McpServerRef{{
		Name:    "stub",
		Command: "/abs/stub",
		Args:    []string{"--stdio"},
		Env:     []acp.EnvVariable{{Name: "A", Value: "1"}},
	}})
	m, ok := got["stub"]
	if !ok {
		t.Fatalf("manifest missing: %#v", got)
	}
	if m.Transport != "stdio" {
		t.Errorf("Transport = %q, want stdio", m.Transport)
	}
	if m.Command != "/abs/stub" || len(m.Args) != 1 || m.Args[0] != "--stdio" {
		t.Errorf("command/args not preserved: %+v", m)
	}
	if m.Env["A"] != "1" {
		t.Errorf("env not preserved: %#v", m.Env)
	}
	if got := mcpManifestsFromACP(nil); got != nil {
		t.Errorf("empty refs = %#v, want nil", got)
	}
}
