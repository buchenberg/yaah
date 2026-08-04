// Package assistant renders assistant messages.
package assistant

import (
	"strings"

	"charm.land/glamour/v2"
	"github.com/buchenberg/yaah/internal/tui2/colors"
)

var mdRenderer *glamour.TermRenderer

func init() {
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(80),
		glamour.WithEmoji(),
	)
	if err == nil {
		mdRenderer = r
	}
}

// Render formats an assistant message. If glamour markdown
// rendering is available the content is passed through it;
// otherwise plain text is used.
func Render(msg string) string {
	prefix := "[#00d787]Yaah: " + colors.Reset
	if mdRenderer != nil {
		out, err := mdRenderer.Render(msg)
		if err == nil {
			return prefix + strings.TrimSpace(out) + "\n"
		}
	}
	return prefix + msg + "\n"
}
