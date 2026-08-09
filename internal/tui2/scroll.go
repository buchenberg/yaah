package tui2

import (
	"context"
	"log"
	"time"

	"github.com/buchenberg/yaah/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/tui2/components/messages"
)

// markDirty sets the refresh flag. Call instead of refreshMessages()
// to coalesce multiple rapid updates into a single render pass.
func (t *TUI2) markDirty() {
	if !t.needsRefresh.Swap(true) {
		t.requestRefresh()
	}
}

// requestRefresh enqueues a single refresh callback. If refresh work is
// already queued, this is a no-op. If new updates arrive while a refresh
// callback is running, another callback is scheduled after it completes.
func (t *TUI2) requestRefresh() {
	if !t.refreshQueued.CompareAndSwap(false, true) {
		return
	}

	t.queueUpdateDraw(func() {
		t.flushRefresh()
		t.refreshQueued.Store(false)
		if t.needsRefresh.Load() {
			t.requestRefresh()
		}
	})
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
	now := time.Now()
	var cadence time.Duration
	if prev := t.lastRefreshUnixNano.Swap(now.UnixNano()); prev > 0 {
		cadence = now.Sub(time.Unix(0, prev))
	}
	queueDepth := t.uiQueueDepth()

	_, span := otel.Tracer("yaah").Start(context.Background(), "tui2.refresh",
		trace.WithAttributes(
			attribute.Int("items", n),
			attribute.Int("msg_bytes", len(msg)),
			attribute.Int64("dur_total_us", totalDur.Microseconds()),
			attribute.Int64("dur_format_us", formatDur.Microseconds()),
			attribute.Int64("dur_settext_us", setDur.Microseconds()),
			attribute.Int("queue_depth", queueDepth),
			attribute.Int64("ui_event_drops", t.uiEventDrops.Load()),
			attribute.Int64("ui_event_fallbacks", t.uiEventFallbacks.Load()),
		))
	span.End()
	observability.RecordTUIRefresh(context.Background(), totalDur, cadence, queueDepth)

	if totalDur > 50*time.Millisecond {
		log.Printf("SLOW refreshMessages: %d items, %d bytes, total=%v format=%v settext=%v",
			n, len(msg), totalDur, formatDur, setDur)
	}
}
