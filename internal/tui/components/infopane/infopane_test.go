package infopane

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/components/mcpinfo"
)

func TestFormat_Basic(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		Provider: "openai",
		Model:    "gpt-4",
		Version:  "1.0.0",
	}
	out := Format(s, &th)
	if out == "" {
		t.Error("Format should not return empty")
	}
	if !strings.Contains(out, "openai") {
		t.Errorf("should contain provider, got %q", out)
	}
	if !strings.Contains(out, "gpt-4") {
		t.Errorf("should contain model, got %q", out)
	}
}

func TestFormat_AgentActive(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{AgentActive: true}
	out := Format(s, &th)
	if !strings.Contains(out, "active") {
		t.Errorf("should show active when AgentActive=true, got %q", out)
	}
}

func TestFormat_AgentIdle(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{AgentActive: false}
	out := Format(s, &th)
	if !strings.Contains(out, "idle") {
		t.Errorf("should show idle when AgentActive=false, got %q", out)
	}
}

func TestFormat_ContextInfo(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{ContextTokens: 50000, ContextWindow: 128000}
	out := Format(s, &th)
	if !strings.Contains(out, "128000") {
		t.Errorf("should contain context window, got %q", out)
	}
}

func TestFormat_McpServers(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		McpServers: []mcpinfo.Server{
			{Name: "filesystem", Connected: true},
			{Name: "database", Connected: false},
		},
	}
	out := Format(s, &th)
	if !strings.Contains(out, "filesystem") {
		t.Errorf("should contain MCP server name, got %q", out)
	}
}

func TestFormat_SubAgentsEnabled(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		SubAgents: SubAgentInfo{
			Enabled:     true,
			Provider:    "openai",
			Model:       "gpt-4",
			Concurrency: 3,
		},
	}
	out := Format(s, &th)
	if !strings.Contains(out, "openai") {
		t.Errorf("should contain sub-agent provider, got %q", out)
	}
	if strings.Contains(out, "disabled") {
		t.Error("should not say disabled when sub-agents enabled")
	}
}

func TestFormat_SubAgentsDisabled(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		SubAgents: SubAgentInfo{Enabled: false},
	}
	out := Format(s, &th)
	if !strings.Contains(out, "disabled") {
		t.Errorf("should say disabled, got %q", out)
	}
}

func TestFormat_EmbeddingEnabled(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		Embedding: EmbeddingInfo{Enabled: true, Model: "text-embedding-3-small"},
	}
	out := Format(s, &th)
	if !strings.Contains(out, "active") {
		t.Errorf("should show active for enabled embedding, got %q", out)
	}
}

func TestFormat_EmbeddingDisabled(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		Embedding: EmbeddingInfo{Enabled: false},
	}
	out := Format(s, &th)
	if !strings.Contains(out, "inactive") {
		t.Errorf("should show inactive for disabled embedding, got %q", out)
	}
}

func TestFormat_Pipeline(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		Pipeline: []string{"approval", "compaction"},
	}
	out := Format(s, &th)
	if !strings.Contains(out, "approval") {
		t.Errorf("should contain pipeline entry, got %q", out)
	}
	if !strings.Contains(out, "compaction") {
		t.Errorf("should contain second pipeline entry, got %q", out)
	}
}

func TestFormat_PipelineEmpty(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{Pipeline: nil}
	out := Format(s, &th)
	if strings.Contains(out, "Middleware") {
		t.Error("should not show middleware section when pipeline empty")
	}
}

func TestFormat_EphemeralMsg(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{EphemeralMsg: "Copied!"}
	out := Format(s, &th)
	if !strings.Contains(out, "Copied!") {
		t.Errorf("should contain ephemeral message, got %q", out)
	}
}

func TestFormat_EphemeralMsgEmpty(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{EphemeralMsg: ""}
	out := Format(s, &th)
	if strings.Contains(out, "Copied!") {
		t.Error("should not show ephemeral message when empty")
	}
}
