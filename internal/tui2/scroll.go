package tui2

import (
	"context"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/tui2/components/messages"
)

// markDirty sets the refresh flag. Call instead of refreshMessages()
// to coalesce multiple rapid updates into a single render pass.
func (t *TUI2) markDirty() {
	t.needsRefresh.Store(true)
}

// flushRefresh performs the actual render if dirty, then clears the flag.
func (t *TUI2) flushRefresh() {
	if t.needsRefresh.Swap(false) {
		t.refreshMessages()
	}
}

// refreshMessages re-renders the conversation text view from the current
// conversation log. If the user has not scrolled up (t.userScrolled is
// false), it auto-scrolls to the end; otherwise it preserves the current
// viewport position.
func (t *TUI2) refreshMessages() {
	start := time.Now()
	w := messageWidth(t.Messages)
	n := len(t.conversationLog)

	items := make([]messages.Item, n)
	for i := range t.conversationLog {
		ci := &t.conversationLog[i]
		var text string
		if ci.text != "" {
			if ci.isMarkdown {
				if ci.cached == "" || ci.cachedWidth != w {
					ci.cached = renderMarkdown(ci.text, w, t.Theme)
					ci.cachedWidth = w
				}
				text = ci.cached
			} else {
				text = ci.text
			}
		}
		items[i] = messages.Item{
			Text:      text,
			ToolBlock: ci.toolBlock,
			SubBlock:  ci.subBlock,
			ReasBlock: ci.reasoningBlock,
		}
	}

	formatStart := time.Now()
	msg := messages.Format(items, "", messages.Content{
		Width: w,
		Theme: t.Theme,
	})
	formatDur := time.Since(formatStart)

	if t.thinkingInd.Visible() {
		msg += "\n  " + t.thinkingInd.Render()
	}
	t.charsRendered.Store(int64(len(msg)))

	setStart := time.Now()
	t.Messages.SetText(msg)
	setDur := time.Since(setStart)

	if !t.userScrolled {
		t.Messages.ScrollToEnd()
	}

	totalDur := time.Since(start)

	_, span := otel.Tracer("yaah").Start(context.Background(), "tui2.refresh",
		trace.WithAttributes(
			attribute.Int("items", n),
			attribute.Int("msg_bytes", len(msg)),
			attribute.Int64("dur_total_us", totalDur.Microseconds()),
			attribute.Int64("dur_format_us", formatDur.Microseconds()),
			attribute.Int64("dur_settext_us", setDur.Microseconds()),
		))
	span.End()

	if totalDur > 50*time.Millisecond {
		log.Printf("SLOW refreshMessages: %d items, %d bytes, total=%v format=%v settext=%v",
			n, len(msg), totalDur, formatDur, setDur)
	}
}
