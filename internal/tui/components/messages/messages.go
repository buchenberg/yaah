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

// emitItem appends the rendered form of a single item to b. Format and
// itemRows share it, so rendered rows and displayed rows cannot drift.
func emitItem(b *strings.Builder, item *Item, rctx colors.RenderCtx) {
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

// Format renders a list of conversation items into the messages pane
// buffer. The thinking indicator is no longer embedded here — it lives
// in the dedicated activity line widget.
func Format(items []Item, ctx Content) string {
	var b strings.Builder
	rctx := colors.RenderCtx{Width: ctx.Width, Theme: ctx.Theme}

	for i := range items {
		emitItem(&b, &items[i], rctx)
	}

	return b.String()
}

// ItemRows reports how many screen rows the emitted text for item
// occupies when wrapped to ctx.Width. Style tags are counted as zero
// width (tview's WordWrap parses them), matching draw-time wrapping.
// The trailing row a newline hands the next item is pending, not
// painted, and is not counted.
func ItemRows(item Item, ctx Content) int {
	var b strings.Builder
	emitItem(&b, &item, colors.RenderCtx{Width: ctx.Width, Theme: ctx.Theme})
	return rowsOf(b.String(), ctx.Width)
}

// rowsOf counts painted rows for a chunk: tview's word wrap over the
// tagged text, minus the one pending row a trailing newline creates.
func rowsOf(tagged string, width int) int {
	if width <= 0 {
		width = 80
	}
	lines := tview.WordWrap(tagged, width)
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return len(lines)
}
