// Package mcp builds the MCP server status panel.
package mcp

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// ServerEntry holds a single MCP server status.
type ServerEntry struct {
	Name      string
	Connected bool
}

const (
	iconOnline  = "●"
	iconOffline = "●"
)

// Build creates the MCP server status panel and returns its initial server list.
func Build() (*tview.TextView, []ServerEntry) {
	tv := tview.NewTextView().
		SetTextAlign(tview.AlignRight).
		SetDynamicColors(true).
		SetWordWrap(false)
	tv.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" MCP ")

	servers := []ServerEntry{
		{Name: "github.com/modelcontextprotocol/servers", Connected: true},
		{Name: "postgres", Connected: false},
		{Name: "filesystem", Connected: false},
	}

	tv.SetText(formatServers(servers))
	return tv, servers
}

// Update rebuilds the MCP status text from the server list.
func Update(tv *tview.TextView, servers []ServerEntry) {
	tv.SetText(formatServers(servers))
}

func formatServers(servers []ServerEntry) string {
	var b strings.Builder
	for i, s := range servers {
		if i > 0 {
			b.WriteString("\n")
		}
		if s.Connected {
			b.WriteString(colors.Tag(colors.Accent, fmt.Sprintf("%s connected  %s", iconOnline, s.Name)))
		} else {
			b.WriteString(colors.Tag(colors.Dim, fmt.Sprintf("%s offline    %s", iconOffline, s.Name)))
		}
	}
	return b.String()
}
