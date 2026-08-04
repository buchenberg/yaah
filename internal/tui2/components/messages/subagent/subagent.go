// Package subagent renders sub-agent start/end messages.
package subagent

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Start returns a formatted line for when a sub-agent begins.
func Start(msg string) string {
	return fmt.Sprintf("%s🤖 %s%s\n", colors.Dim, msg, colors.Reset)
}

// End returns a formatted line for when a sub-agent completes.
func End(msg string) string {
	return fmt.Sprintf("%s🤖 %s %s%s\n",
		colors.Dim, msg, colors.Reset, colors.Dim+"done"+colors.Reset,
	)
}

// Render is a generic sub-agent message formatter.
func Render(msg string) string {
	return fmt.Sprintf("%s🤖 %s%s\n", colors.Dim, msg, colors.Reset)
}
