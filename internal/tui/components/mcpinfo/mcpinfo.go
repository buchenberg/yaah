// Package mcpinfo formats the MCP server status section.
package mcpinfo

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui/colors"
)

// Server holds a single MCP server state.
type Server struct {
	Name      string
	Connected bool
}

// Format returns a formatted MCP server status block.
func Format(servers []Server, th *colors.Theme) string {
	if len(servers) == 0 {
		return th.Tag(th.Dim, "  (no servers)\n")
	}

	var b strings.Builder
	for _, s := range servers {
		if s.Connected {
			b.WriteString(fmt.Sprintf("  %s %s\n",
				th.Tag(th.Connected, "●"),
				th.Tag(th.Detail, s.Name),
			))
		} else {
			b.WriteString(fmt.Sprintf("  %s %s\n",
				th.Tag(th.Dim, "●"),
				th.Tag(th.Dim, s.Name),
			))
		}
	}
	return b.String()
}
