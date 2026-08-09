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
func Format(s Info, th *colors.Theme) string {
	var b strings.Builder
	b.WriteString(th.TagBold(th.Heading, "Session\n"))
	b.WriteString(fmt.Sprintf("  Provider: %s\n", th.Tag(th.Detail, s.Provider)))
	b.WriteString(fmt.Sprintf("  Model: %s\n", th.Tag(th.Detail, s.Model)))
	b.WriteString(fmt.Sprintf("  Agent: %s\n", th.Tag(th.Detail, shortVersion(s.Version))))
	return b.String()
}

func shortVersion(v string) string {
	if idx := strings.IndexByte(v, '-'); idx > 0 {
		return v[:idx]
	}
	return v
}
