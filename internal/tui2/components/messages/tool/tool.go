// Package tool renders tool execution messages.
package tool

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Start returns a formatted line for when a tool begins executing.
func Start(name, args string) string {
	return fmt.Sprintf("%s🔧 %s %s%s\n",
		colors.Dim, name, colors.Reset, colors.Dim+args+colors.Reset,
	)
}

// End returns a formatted line for when a tool completes.
func End(name string) string {
	return fmt.Sprintf("%s✅ %s done%s\n",
		colors.Dim, name, colors.Reset,
	)
}

// Summary returns a one-line summary of a completed tool call.
func Summary(name, result string) string {
	return fmt.Sprintf("%s✅ %s %s%s\n",
		colors.Dim, name, colors.Reset,
		colors.Tag(colors.Accent, result),
	)
}

// Render is a generic tool message formatter.
func Render(msg string) string {
	return fmt.Sprintf("%s🔧 %s%s\n", colors.Dim, msg, colors.Reset)
}
