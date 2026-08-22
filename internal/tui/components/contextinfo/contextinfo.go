// Package contextinfo formats the context-window / token usage section.
package contextinfo

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui/colors"
)

// Format returns a formatted context info block showing tokens used vs
// the context window, or a dash placeholder when the window is unknown.
func Format(tokens, window int, th *colors.Theme) string {
	if window > 0 {
		pct := float64(tokens) * 100 / float64(window)
		if pct > 100 {
			pct = 100
		}
		return th.TagBold(th.Heading, "Context\n") +
			fmt.Sprintf("  Used: %s\n", th.Tag(th.Detail,
				fmt.Sprintf("%d / %d (%.1f%%)", tokens, window, pct)))
	}
	return th.TagBold(th.Heading, "Context\n") +
		fmt.Sprintf("  Used: %s\n", th.Tag(th.Detail, "\u2500"))
}
