// Package sessioninfo formats the agent info section.
package sessioninfo

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui/colors"
)

// Info holds the current agent details.
type Info struct {
	Model    string
	Provider string
	Version  string
	Status   string
}

// Format returns a formatted agent info block.
func Format(s Info, th *colors.Theme) string {
	var b strings.Builder
	b.WriteString(th.TagBold(th.Heading, "Agent\n"))
	b.WriteString(fmt.Sprintf("  Provider: %s\n", th.Tag(th.Detail, s.Provider)))
	b.WriteString(fmt.Sprintf("  Model: %s\n", th.Tag(th.Detail, s.Model)))
	b.WriteString(fmt.Sprintf("  Version: %s\n", th.Tag(th.Detail, shortVersion(s.Version))))
	b.WriteString(fmt.Sprintf("  Status: %s\n", th.Tag(th.Detail, s.Status)))
	return b.String()
}

func shortVersion(v string) string {
	if idx := strings.IndexByte(v, '-'); idx > 0 {
		return v[:idx]
	}
	return v
}
