package tui2

import (
	"github.com/buchenberg/tviewmd"
)

// renderMarkdown converts markdown to tview color-tagged text using the
// tviewmd native renderer (goldmark parser + tview tag backend). The
// output is suitable for a tview.TextView with SetWrap(true).
func renderMarkdown(md string) string {
	if md == "" {
		return ""
	}
	return tviewmd.Render(md, tviewmd.Options{Width: 80})
}
