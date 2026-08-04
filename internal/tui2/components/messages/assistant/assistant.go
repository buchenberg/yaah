// Package assistant renders assistant messages.
package assistant

import (
	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Render formats an assistant message. If glamour markdown
// rendering is available the content is passed through it;
// otherwise plain text is used.
func Render(msg string) string {
	return "[#00d787]Yaah: " + colors.Reset + msg + "\n"
}
