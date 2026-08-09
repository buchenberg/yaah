package tui2

import (
	"github.com/buchenberg/tviewmd"
	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/rivo/tview"
)

func mdTheme(th *colors.Theme) tviewmd.Theme {
	return tviewmd.Theme{
		Heading:      th.MDHeading,
		Link:         th.MDLink,
		InlineCodeFG: th.MDCodeFG,
		InlineCodeBG: "",
		CodeBlockFG:  th.MDCodeFG,
		QuoteFG:      th.MDQuoteFG,
		Hr:           th.MDHr,
	}
}

// renderMarkdown converts markdown to tview color-tagged text. Width
// controls table column sizing. The output uses valid tview tags — no
// raw brackets remain, so SetDynamicColors won't consume them as directives.
func renderMarkdown(md string, width int, th *colors.Theme) string {
	if md == "" {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	if width > 2 {
		width -= 2
	}
	return tviewmd.Render(md, tviewmd.Options{Width: width, Theme: mdTheme(th)})
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
