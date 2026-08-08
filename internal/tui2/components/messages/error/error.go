// Package error renders error messages in red/dim styling.
package error

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Render formats an error message in red and dim styling.
func Render(msg string) string {
	return fmt.Sprintf("[%s][red]✗ Error:%s %s\n", colors.Dim, colors.Reset, msg)
}
