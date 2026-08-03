// Package mcpinfo formats the MCP server status section.
package mcpinfo

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Server holds a single MCP server state.
type Server struct {
	Name      string
	Connected bool
}

// Format returns a formatted MCP server status block.
func Format(servers []Server) string {
	if len(servers) == 0 {
		return colors.Tag(colors.Dim, "  (no servers)\n")
	}

	var b strings.Builder
	for _, s := range servers {
		if s.Connected {
			b.WriteString(fmt.Sprintf("  %s %s\n",
				colors.Tag("#00d787", "●"),
				colors.Tag(colors.Accent, s.Name),
			))
		} else {
			b.WriteString(fmt.Sprintf("  %s %s\n",
				colors.Tag(colors.Dim, "●"),
				colors.Tag(colors.Dim, s.Name),
			))
		}
	}
	return b.String()
}
