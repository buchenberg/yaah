// Package messages builds and renders the conversation message area.
package messages

import (
	"strings"

	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/buchenberg/yaah/internal/tui/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui/components/toolblock"
	"github.com/rivo/tview"
)

// Item is a single entry in the chronological conversation log.
// Exactly one of the fields is set — Text for plain/markdown content,
// ToolBlock for tool calls, or ReasBlock for reasoning sections.
// Sub-agents are not conversation items; they render in their own pane.
type Item struct {
	Text      string
	ToolBlock *toolblock.Block
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

// Format renders a list of conversation items into the messages pane
// buffer. The thinking indicator is no longer embedded here — it lives
// in the dedicated activity line widget.
func Format(items []Item, ctx Content) string {
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
		case item.ReasBlock != nil:
			b.WriteString("\n")
			b.WriteString(item.ReasBlock.RenderCtx(rctx))
			b.WriteString("\n\n")
		}
	}

	return b.String()
}
