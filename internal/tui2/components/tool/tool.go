// Package tool formats tool-execution log lines.
package tool

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Start returns a formatted line for when a tool begins executing.
func Start(name, args string) string {
	return fmt.Sprintf("%s🔧 %s %s%s\n",
		colors.Dim, name, colors.Reset, colors.Dim+args+colors.Reset,
	)
}

// End returns a formatted line for when a tool completes.
func End(name, result string) string {
	return fmt.Sprintf("%s✅ %s done%s\n",
		colors.Dim, name, colors.Reset,
	)
}

// Summary returns a one-line summary of a completed tool call.
func Summary(name, result string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s✅ %s %s%s\n",
		colors.Dim, name, colors.Reset,
		colors.Tag(colors.Accent, result),
	))
	return b.String()
}
