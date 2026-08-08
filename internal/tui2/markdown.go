package tui2

import (
	"github.com/buchenberg/tviewmd"
	"github.com/rivo/tview"
)

// mdTheme is the tviewmd rendering theme. Headings use bold rather than
// underline to prevent style leakage across paragraphs in tview TextViews.
var mdTheme = tviewmd.Theme{
	Heading: [6]string{
		"#00afff",
		"#00afff",
		"#00afff",
		"#00afff",
		"#00afff",
		"#00afff",
	},
	Link:         "#00afff",
	InlineCodeFG: "#5f5f5f",
	InlineCodeBG: "default",
	CodeBlockFG:  "#5f5f5f",
	QuoteFG:      "#5f5f5f",
	Hr:           "#5f5f5f",
}

// renderMarkdown converts markdown to tview color-tagged text. Width
// controls table column sizing. The output uses valid tview tags — no
// raw brackets remain, so SetDynamicColors won't consume them as directives.
func renderMarkdown(md string, width int) string {
	if md == "" {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	return tviewmd.Render(md, tviewmd.Options{Width: width, Theme: mdTheme})
}

// messageWidth returns the inner width of the messages pane, defaulting
// to 80 if the pane hasn't been laid out yet.
func messageWidth(tv *tview.TextView) int {
	if tv == nil {
		return 80
	}
	_, _, w, _ := tv.GetInnerRect()
	if w <= 0 {
		return 80
	}
	return w
}
