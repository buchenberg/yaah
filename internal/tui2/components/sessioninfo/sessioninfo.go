// Package sessioninfo formats the session / model / provider section.
package sessioninfo

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Info holds the current session details.
type Info struct {
	Model    string
	Provider string
	Version  string
}

// Format returns a formatted session info block.
func Format(s Info) string {
	var b strings.Builder
	b.WriteString(colors.TagBold(colors.Accent, "Session\n"))
	b.WriteString(fmt.Sprintf("  Model:    %s\n", colors.Tag(colors.Accent, s.Model)))
	b.WriteString(fmt.Sprintf("  Provider: %s\n", colors.Tag(colors.Accent, s.Provider)))
	b.WriteString(fmt.Sprintf("  Agent:    %s\n", colors.Tag(colors.Accent, s.Version)))
	return b.String()
}
