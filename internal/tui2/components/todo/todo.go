// Package todo builds and formats a todo/task list.
package todo

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Item represents a single todo entry.
type Item struct {
	Text   string
	Done   bool
	Active bool // currently in-progress
}

// Format returns a formatted todo list string suitable for a tview TextView.
func Format(items []Item) string {
	if len(items) == 0 {
		return colors.Tag(colors.Dim, "  (no tasks)\n")
	}

	var b strings.Builder
	for _, it := range items {
		switch {
		case it.Done:
			b.WriteString(fmt.Sprintf("  %s %s\n",
				colors.Tag("#00d787", "✓"),
				colors.Tag(colors.Dim, it.Text),
			))
		case it.Active:
			b.WriteString(fmt.Sprintf("  %s %s\n",
				colors.TagBold(colors.Accent, "▶"),
				colors.TagBold(colors.Accent, it.Text),
			))
		default:
			b.WriteString(fmt.Sprintf("  %s %s\n",
				colors.Tag(colors.Dim, "○"),
				colors.Tag(colors.Reset, it.Text),
			))
		}
	}
	return b.String()
}
