// Package tool formats tool-execution log lines.
package tool

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Start returns a formatted line for when a tool begins executing.
func Start(name, args string) string {
	return fmt.Sprintf("[%s]🔧 %s[-] [%s]%s[-]\n", colors.Dim, name, colors.Dim, args)
}

// End returns a formatted line for when a tool completes.
func End(name, result string) string {
	return fmt.Sprintf("[%s]✅ %s done[-]\n", colors.Dim, name)
}

// Summary returns a one-line summary of a completed tool call.
func Summary(name, result string) string {
	return fmt.Sprintf("[%s]✅ %s[-] %s\n", colors.Dim, name, colors.Tag(colors.Accent, result))
}
