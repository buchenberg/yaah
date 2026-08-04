package tui2

import (
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"github.com/rivo/tview"
)

var (
	rendererOnce sync.Once
	renderer     *glamour.TermRenderer
)

// initRenderer lazily creates a glamour renderer on first use. We use
// terminal256 formatting; the ANSI output is translated to tview color
// tags via tview.TranslateANSI at render time.
func initRenderer() {
	rendererOnce.Do(func() {
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(80),
			glamour.WithEmoji(),
			glamour.WithChromaFormatter("terminal256"),
			glamour.WithPreservedNewLines(),
		)
		if err == nil {
			renderer = r
		}
	})
}

// renderMarkdown converts markdown to tview color-tagged text using
// glamour for formatting and tview.TranslateANSI to convert the ANSI
// output into tview's native color tags. Falls back to the raw text if
// the renderer is unavailable.
func renderMarkdown(md string) string {
	initRenderer()
	if renderer == nil {
		return md
	}
	out, err := renderer.Render(md)
	if err != nil {
		return md
	}
	out = strings.TrimSpace(out)
	return tview.TranslateANSI(out)
}
