package infopane

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/buchenberg/yaah/internal/tui/components/contextinfo"
	"github.com/buchenberg/yaah/internal/tui/components/mcpinfo"
	"github.com/buchenberg/yaah/internal/tui/components/sessioninfo"
	worldsection "github.com/buchenberg/yaah/internal/tui/components/worldinfo"
	"github.com/buchenberg/yaah/internal/worldinfo"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// State carries live data for the info pane.
type State struct {
	Provider              string
	Model                 string
	SessionMode           string
	Version               string
	ContextTokens         int
	ContextWindow         int
	McpServers            []mcpinfo.Server
	EphemeralMsg          string
	SubAgents             SubAgentInfo
	Embedding             EmbeddingInfo
	World                 worldinfo.Status
	Pipeline              []string
	AgentActive           bool
	AgentPromptTokens     int
	AgentCompletionTokens int
	AgentCostEstimate     string
	SubPromptTokens       int
	SubCompletionTokens   int
	SubTotalTokens        int
	SubCachedTokens       int
	SubCostEstimate       string
}

// SubAgentInfo holds sub-agent configuration for display.
type SubAgentInfo struct {
	Enabled     bool
	Provider    string
	Concurrency int
	Model       string
}

// EmbeddingInfo holds embedding configuration for display.
type EmbeddingInfo struct {
	Enabled bool
	Model   string
}

// Build creates the bordered TextView for the info pane with the given
// border color hex (e.g. "#ff00ff").
func Build(borderColor string) *tview.TextView {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(true)
	tv.SetBorder(true)
	tv.SetTitle(" Info ")
	if borderColor != "" {
		tv.SetBorderColor(tcell.GetColor(borderColor))
		tv.SetTitleColor(tcell.GetColor(borderColor))
	}
	return tv
}

// Format assembles the full info pane content. Pane order: version header,
// Agent (with the dissolved workspace/scope posture), Context, Usage, the
// Subagents posture (with the dissolved containment/dispatch/network rows),
// Subagents Usage, then MCP, Embedding, Middleware, and Ephemeral.
func Format(s State, th *colors.Theme) string {
	var b strings.Builder

	// Version header.
	b.WriteString(sessioninfo.Header(s.Version, th))
	b.WriteString("\n")

	// Agent: provider/model/status/mode + workspace/scope posture.
	status := "idle"
	if s.AgentActive {
		status = "active"
	}
	b.WriteString(th.TagBold(th.Heading, "Agent\n"))
	b.WriteString(sessioninfo.Format(sessioninfo.Info{
		Provider: s.Provider,
		Model:    s.Model,
		Status:   status,
	}, th))
	if s.SessionMode != "" {
		b.WriteString(fmt.Sprintf("  Mode: %s\n", th.Tag(th.Detail, s.SessionMode)))
	}
	b.WriteString(worldsection.AgentRows(s.World, th))
	b.WriteString("\n")

	b.WriteString(contextinfo.Format(s.ContextTokens, s.ContextWindow, th))
	b.WriteString("\n")

	// Usage: the orchestrator's own spend (session total minus sub-agents).
	if s.AgentPromptTokens > 0 || s.AgentCompletionTokens > 0 {
		b.WriteString(th.TagBold(th.Heading, "Usage\n"))
		b.WriteString(fmt.Sprintf("  Prompt: %s\n", th.Tag(th.Detail, fmt.Sprintf("%d", s.AgentPromptTokens))))
		b.WriteString(fmt.Sprintf("  Completion: %s\n", th.Tag(th.Detail, fmt.Sprintf("%d", s.AgentCompletionTokens))))
		if s.AgentCostEstimate != "" {
			b.WriteString(fmt.Sprintf("  Cost: %s\n", th.Tag(th.Detail, s.AgentCostEstimate)))
		}
		b.WriteString("\n")
	}

	// Subagents: provider/model/concurrency + containment/dispatch/network.
	if s.SubAgents.Enabled || !s.World.IsZero() {
		b.WriteString(th.TagBold(th.Heading, "Subagents\n"))
		if s.SubAgents.Enabled {
			b.WriteString(fmt.Sprintf("  Provider: %s\n", th.Tag(th.Detail, s.SubAgents.Provider)))
			b.WriteString(fmt.Sprintf("  Model: %s\n", th.Tag(th.Detail, s.SubAgents.Model)))
			b.WriteString(fmt.Sprintf("  Concurrency: %d\n", s.SubAgents.Concurrency))
		} else {
			b.WriteString(th.Tag(th.Dim, "  (disabled)\n"))
		}
		b.WriteString(worldsection.SubAgentRows(s.World, th))
		b.WriteString("\n")
	}

	// Subagents Usage: sub-agent spend, split from the orchestrator Usage.
	if s.SubPromptTokens > 0 || s.SubCompletionTokens > 0 {
		b.WriteString(th.TagBold(th.Heading, "Subagents Usage\n"))
		b.WriteString(fmt.Sprintf("  Prompt: %s\n", th.Tag(th.Detail, fmt.Sprintf("%d", s.SubPromptTokens))))
		b.WriteString(fmt.Sprintf("  Completion: %s\n", th.Tag(th.Detail, fmt.Sprintf("%d", s.SubCompletionTokens))))
		b.WriteString(fmt.Sprintf("  Total: %s\n", th.Tag(th.Detail, fmt.Sprintf("%d", s.SubTotalTokens))))
		if s.SubCachedTokens > 0 {
			b.WriteString(fmt.Sprintf("  Cached: %s\n", th.Tag(th.Detail, fmt.Sprintf("%d", s.SubCachedTokens))))
		}
		if s.SubCostEstimate != "" {
			b.WriteString(fmt.Sprintf("  Cost (approx): %s\n", th.Tag(th.Detail, s.SubCostEstimate)))
		}
		b.WriteString("\n")
	}

	b.WriteString(th.TagBold(th.Heading, "MCP\n"))
	b.WriteString(mcpinfo.Format(s.McpServers, th))
	b.WriteString("\n")

	if s.Embedding.Enabled {
		b.WriteString(th.TagBold(th.Heading, "Embedding\n"))
		b.WriteString(fmt.Sprintf("  Status: %s\n", th.Tag(th.Detail, "active")))
	} else {
		b.WriteString(th.TagBold(th.Heading, "Embedding\n"))
		b.WriteString(th.Tag(th.Dim, "  (inactive)\n"))
	}

	if len(s.Pipeline) > 0 {
		b.WriteString("\n")
		b.WriteString(th.TagBold(th.Heading, "Middleware\n"))
		for _, name := range s.Pipeline {
			b.WriteString(fmt.Sprintf("  %s %s\n", th.Tag(th.Dim, "→"), th.Tag(th.Detail, name)))
		}
	}

	if s.EphemeralMsg != "" {
		b.WriteString("\n")
		b.WriteString(th.Tag(th.Heading, s.EphemeralMsg))
		b.WriteString("\n")
	}

	return b.String()
}
