// Package system renders system messages in dim styling.
package system

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Render formats a system message in dim styling.
func Render(msg string) string {
	return fmt.Sprintf("%s[system] %s[-]\n", colors.Dim, msg)
}
