package infopane

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/components/contextinfo"
	"github.com/buchenberg/yaah/internal/tui2/components/mcpinfo"
	"github.com/buchenberg/yaah/internal/tui2/components/sessioninfo"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// State carries live data for the info pane.
type State struct {
	Provider      string
	Model         string
	Version       string
	ContextTokens int
	ContextWindow int
	McpServers    []mcpinfo.Server
	EphemeralMsg  string
	SubAgents     SubAgentInfo
	Embedding     EmbeddingInfo
	Pipeline      []string
	AgentActive   bool
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

// Format assembles the full info pane content: session, context, MCP, and
// ephemeral message.
func Format(s State, th *colors.Theme) string {
	var b strings.Builder

	b.WriteString(sessioninfo.Format(sessioninfo.Info{
		Provider: s.Provider,
		Model:    s.Model,
		Version:  s.Version,
	}, th))
	b.WriteString("\n\n")

	b.WriteString(th.TagBold(th.Heading, "Agent\n"))
	if s.AgentActive {
		b.WriteString(fmt.Sprintf("  Status: %s\n", th.Tag(th.Connected, "active")))
	} else {
		b.WriteString(fmt.Sprintf("  Status: %s\n", th.Tag(th.Dim, "idle")))
	}
	b.WriteString("\n")

	b.WriteString(contextinfo.Format(s.ContextTokens, s.ContextWindow, th))
	b.WriteString("\n")

	b.WriteString(th.TagBold(th.Heading, "MCP\n"))
	b.WriteString(mcpinfo.Format(s.McpServers, th))
	b.WriteString("\n")

	if s.SubAgents.Enabled {
		b.WriteString(th.TagBold(th.Heading, "Sub-agents\n"))
		b.WriteString(fmt.Sprintf("  Provider: %s\n", th.Tag(th.Detail, s.SubAgents.Provider)))
		b.WriteString(fmt.Sprintf("  Model: %s\n", th.Tag(th.Detail, s.SubAgents.Model)))
		b.WriteString(fmt.Sprintf("  Concurrency: %s\n", th.Tag(th.Detail, fmt.Sprintf("%d", s.SubAgents.Concurrency))))
	} else {
		b.WriteString(th.TagBold(th.Heading, "Sub-agents\n"))
		b.WriteString(th.Tag(th.Dim, "  (disabled)\n"))
	}
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
