// Package contextinfo formats the context-window / token usage section.
package contextinfo

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Info holds context-window and token usage stats.
type Info struct {
	Used      string // e.g. "12.4K / 128K"
	TokensIn  string // e.g. "4.2K"
	TokensOut string // e.g. "1.8K"
}

// Format returns a formatted context info block.
func Format(c Info) string {
	var b strings.Builder
	b.WriteString(colors.TagBold(colors.Accent, "Context\n"))
	b.WriteString(fmt.Sprintf("  Used:  %s\n", colors.Tag(colors.Dim, c.Used)))
	b.WriteString(fmt.Sprintf("  Tokens: %s\n",
		colors.Tag(colors.Dim, fmt.Sprintf("▲ %s  ▼ %s", c.TokensIn, c.TokensOut)),
	))
	return b.String()
}
