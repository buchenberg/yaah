// Package subagent renders sub-agent start/end messages.
package subagent

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Render formats a sub-agent start/end message with a robot icon
// and role-based accent coloring.
func Render(msg string) string {
	return fmt.Sprintf("[%s]🤖 %s%s\n", colors.Dim, msg, colors.Reset)
}
