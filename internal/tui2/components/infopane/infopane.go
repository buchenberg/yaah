package infopane

import (
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/components/contextinfo"
	"github.com/buchenberg/yaah/internal/tui2/components/mcpinfo"
	"github.com/buchenberg/yaah/internal/tui2/components/sessioninfo"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Build creates an empty right-side info pane with a subtle dark
// background. Its content is populated at runtime from live
// control-channel data by TUI2.renderInfoPane.
func Build() *tview.TextView {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(true)
	tv.SetBackgroundColor(tcell.Color236) // dark gray-blue (#303030)
	tv.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Info ")

	var b strings.Builder

	b.WriteString(sessioninfo.Format(sessioninfo.Info{
		Model:    "gpt-4o",
		Provider: "openai",
		Version:  "yaah v0.7.0",
	}))
	b.WriteString("\n")

	b.WriteString(contextinfo.Format(contextinfo.Info{
		Used:      "12.4K / 128K",
		TokensIn:  "4.2K",
		TokensOut: "1.8K",
	}))
	b.WriteString("\n")

	b.WriteString(colors.TagBold(colors.Accent, "MCP\n"))
	b.WriteString(mcpinfo.Format([]mcpinfo.Server{
		{Name: "github.com/modelcontextprotocol/servers", Connected: true},
		{Name: "postgres", Connected: false},
		{Name: "filesystem", Connected: false},
	}))

	tv.SetText(b.String())
	return tv
}
