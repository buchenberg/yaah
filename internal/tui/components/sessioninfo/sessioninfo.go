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
	Status   string
}

// Format returns the agent info rows (provider, model, status). The section
// heading and the version header are rendered by the info pane so the version
// can be promoted above the Agent block and the World posture rows can be
// folded into it.
func Format(s Info, th *colors.Theme) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  Provider: %s\n", th.Tag(th.Detail, s.Provider)))
	b.WriteString(fmt.Sprintf("  Model: %s\n", th.Tag(th.Detail, s.Model)))
	b.WriteString(fmt.Sprintf("  Status: %s\n", th.Tag(th.Detail, s.Status)))
	return b.String()
}

// Header renders the version as a bold header line at the top of the pane.
// Dev builds (no version baked in) render as "Yaah (dev)"; releases render as
// "Yaah v<semver>".
func Header(version string, th *colors.Theme) string {
	v := shortVersion(strings.TrimPrefix(version, "v"))
	switch v {
	case "", "0.0.0", "dev", "(devel)":
		return th.TagBold(th.Heading, "Yaah (dev)")
	default:
		return th.TagBold(th.Heading, "Yaah v"+v)
	}
}

func shortVersion(v string) string {
	if idx := strings.IndexByte(v, '-'); idx > 0 {
		return v[:idx]
	}
	return v
}
