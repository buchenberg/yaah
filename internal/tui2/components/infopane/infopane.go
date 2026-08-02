// Package infopane builds the right-side information panel.
package infopane

import (
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/components/contextinfo"
	"github.com/buchenberg/yaah/internal/tui2/components/mcpinfo"
	"github.com/buchenberg/yaah/internal/tui2/components/sessioninfo"
	"github.com/buchenberg/yaah/internal/tui2/components/todo"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Build creates the right-side info pane with session, context, tasks, and MCP.
func Build() *tview.TextView {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(true)
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
	b.WriteString("\n")

	b.WriteString(colors.TagBold(colors.Accent, "Tasks\n"))
	b.WriteString(todo.Format([]todo.Item{
		{Text: "Move TUI2 to own directory", Done: true},
		{Text: "Break into component files", Done: true},
		{Text: "Add right-side info pane", Active: true},
		{Text: "Wire to agent loop"},
		{Text: "Polish theme & colors"},
	}))

	tv.SetText(b.String())
	return tv
}
