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
		AgentTotalTokens:      150,
		AgentCachedTokens:     30,
		SubPromptTokens:       200,
		SubCompletionTokens:   100,
		SubTotalTokens:        300,
		SubCachedTokens:       20,
	}
	out := Format(s, &th)
	if !strings.Contains(out, "Usage") {
		t.Errorf("should render a Usage section, got %q", out)
	}
	if !strings.Contains(out, "Subagents Usage") {
		t.Errorf("should render a Subagents Usage section, got %q", out)
	}
	if strings.Count(out, "Total:") < 2 {
		t.Errorf("should render a Total row in both Usage sections, got %q", out)
	}
	if strings.Count(out, "Cached:") < 2 {
		t.Errorf("should render a Cached row in both Usage sections, got %q", out)
	}
	if strings.Contains(out, "Cost") {
		t.Errorf("should render no cost rows, got %q", out)
	}
}

func TestFormat_UsageCachedHiddenWhenZero(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		AgentPromptTokens:     100,
		AgentCompletionTokens: 50,
		AgentTotalTokens:      150,
		SubPromptTokens:       200,
		SubCompletionTokens:   100,
		SubTotalTokens:        300,
	}
	out := Format(s, &th)
	if strings.Contains(out, "Cached:") {
		t.Errorf("should hide Cached rows when zero, got %q", out)
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

func TestFormat_TotalFallsBackToPromptPlusCompletion(t *testing.T) {
	th := colors.NewDarkTheme()
	s := State{
		AgentPromptTokens:     100,
		AgentCompletionTokens: 50, // provider omitted total_tokens
		SubPromptTokens:       20,
		SubCompletionTokens:   10,
	}
	out := Format(s, &th)
	if strings.Contains(out, "Total: "+th.Tag(th.Detail, "0")) {
		t.Errorf("should not render Total: 0 when prompt/completion are non-zero, got %q", out)
	}
	if !strings.Contains(out, th.Tag(th.Detail, "150")) {
		t.Errorf("agent Total should fall back to 100+50=150, got %q", out)
	}
	if !strings.Contains(out, th.Tag(th.Detail, "30")) {
		t.Errorf("sub Total should fall back to 20+10=30, got %q", out)
	}
}

func TestUsageTotal(t *testing.T) {
	if got := usageTotal(100, 50, 0); got != 150 {
		t.Errorf("usageTotal(100, 50, 0) = %d, want fallback 150", got)
	}
	if got := usageTotal(100, 50, 999); got != 999 {
		t.Errorf("usageTotal(100, 50, 999) = %d, want reported 999", got)
	}
	if got := usageTotal(0, 0, 0); got != 0 {
		t.Errorf("usageTotal(0, 0, 0) = %d, want 0", got)
	}
}
