// Package tool renders tool execution messages.
package tool

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Render formats a tool execution message with a status icon
// and dim styling.
func Render(msg string) string {
	return fmt.Sprintf("%s🔧 %s%s\n", colors.Dim, msg, colors.Reset)
}