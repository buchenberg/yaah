// Package messages builds and renders the conversation message area.
package messages

import (
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui2/components/subagent"
	"github.com/buchenberg/yaah/internal/tui2/components/toolblock"
	"github.com/rivo/tview"
)

// Item is a single entry in the chronological conversation log.
// Exactly one of the fields is set — Text for plain/markdown content,
// ToolBlock for tool calls, SubBlock for sub-agent rows, or ReasBlock for
// reasoning sections.
type Item struct {
	Text      string
	ToolBlock *toolblock.Block
	SubBlock  *subagent.Block
	ReasBlock *reasoning.Block
}

// Content carries rendering context.
type Content struct {
	Width int
	Theme *colors.Theme
}

// Build creates the scrollable conversation view.
func Build() *tview.TextView {
	tv := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetWordWrap(true)
	return tv
}

// Format renders a list of conversation items with optional thinking
// indicator text. Callers include the thinking spinner by passing a
// non-empty thinkingText.
func Format(items []Item, thinkingText string, ctx Content) string {
	var b strings.Builder
	rctx := colors.RenderCtx{Width: ctx.Width, Theme: ctx.Theme}

	for i := range items {
		item := &items[i]
		switch {
		case item.Text != "":
			b.WriteString("\n")
			b.WriteString(item.Text)
			b.WriteString("\n\n")
		case item.ToolBlock != nil:
			b.WriteString(item.ToolBlock.RenderCtx(rctx))
			b.WriteString("\n")
		case item.SubBlock != nil:
			b.WriteString(item.SubBlock.RenderCtx(rctx))
			b.WriteString("\n")
		case item.ReasBlock != nil:
			b.WriteString("\n")
			b.WriteString(item.ReasBlock.RenderCtx(rctx))
			b.WriteString("\n\n")
		}
	}

	if thinkingText != "" {
		b.WriteString("\n")
		b.WriteString(thinkingText)
		b.WriteString("\n")
	}

	return b.String()
}
