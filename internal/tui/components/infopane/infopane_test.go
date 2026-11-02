package infopane

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/buchenberg/yaah/internal/tui/components/mcpinfo"
	worldstatus "github.com/buchenberg/yaah/internal/worldinfo"
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

func TestFormat_VersionHeader(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{Version: "1.0.0"}
	out := Format(s, &th)
	if !strings.Contains(out, "Yaah v1.0.0") {
		t.Errorf("should render a version header, got %q", out)
	}
	if strings.Contains(out, "Version:") {
		t.Errorf("should not render a Version row, got %q", out)
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

func TestFormat_SubAgentsDisabledNoWorld(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{SubAgents: SubAgentInfo{Enabled: false}}
	out := Format(s, &th)
	if strings.Contains(out, "disabled") {
		t.Errorf("should hide the sub-agents block when disabled and no posture, got %q", out)
	}
	if strings.Contains(out, "Subagents") {
		t.Errorf("should hide the sub-agents block when disabled and no posture, got %q", out)
	}
}

func TestFormat_SubAgentsDisabledWithWorld(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		SubAgents: SubAgentInfo{Enabled: false},
		World: worldstatus.Status{
			Backend:       "local",
			Mode:          "retained",
			Containment:   "auto",
			Network:       "allow_all",
			WorldReq:      "auto",
			ProposalWorld: "worktree",
			ReportWorld:   "worktree",
		},
	}
	out := Format(s, &th)
	if !strings.Contains(out, "disabled") {
		t.Errorf("should show disabled when posture exists but sub-agents off, got %q", out)
	}
	if !strings.Contains(out, "Containment") {
		t.Errorf("should show the dissolved posture rows, got %q", out)
	}
}

func TestFormat_UsageSplit(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		AgentPromptTokens:     100,
		AgentCompletionTokens: 50,
		AgentCostEstimate:     "$0.0001 (at current model rates)",
		SubPromptTokens:       200,
		SubCompletionTokens:   100,
		SubTotalTokens:        300,
		SubCostEstimate:       "$0.0002",
	}
	out := Format(s, &th)
	if !strings.Contains(out, "Usage") {
		t.Errorf("should render a Usage section, got %q", out)
	}
	if !strings.Contains(out, "Subagents Usage") {
		t.Errorf("should render a Subagents Usage section, got %q", out)
	}
	if !strings.Contains(out, "Cost (approx)") {
		t.Errorf("should label sub-agent cost as approx, got %q", out)
	}
	if !strings.Contains(out, "Total") {
		t.Errorf("should render a Total row in Subagents Usage, got %q", out)
	}
}

func TestFormat_UsageHiddenWhenZero(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{}
	out := Format(s, &th)
	if strings.Contains(out, "Subagents Usage") {
		t.Errorf("should hide Subagents Usage when zero, got %q", out)
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
