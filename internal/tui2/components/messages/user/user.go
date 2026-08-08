// Package user renders user messages with accent styling.
package user

import (
	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Render formats a user message with accent styling.
func Render(msg string) string {
	return colors.Tag(colors.Accent, "You: ") + msg + "\n"
}
