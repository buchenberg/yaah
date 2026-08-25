package tui

import (
	"context"
	"io"
	"log"
	"time"

	"github.com/buchenberg/yaah/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/tui/components/messages"
)

// markDirty sets the refresh flag. Call instead of refreshMessages()
// to coalesce multiple rapid updates into a single render pass.
func (t *App) markDirty() {
	if !t.needsRefresh.Swap(true) {
		t.requestRefresh()
	}
}

// requestRefresh enqueues a single refresh callback. If refresh work is
// already queued, this is a no-op. If new updates arrive while a refresh
// callback is running, another callback is scheduled after it completes.
func (t *App) requestRefresh() {
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
func (t *App) flushRefresh() {
	if t.needsRefresh.Swap(false) {
		t.refreshMessages()
	}
}

// buildItems materializes conversationLog[from:to] into render items,
// rendering markdown lazily with per-item width caching.
func (t *App) buildItems(from, to, w int) []messages.Item {
	items := make([]messages.Item, to-from)
	for i := from; i < to; i++ {
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
		items[i-from] = messages.Item{
			Text:      text,
			ToolBlock: ci.toolBlock,
			SubBlock:  ci.subBlock,
			ReasBlock: ci.reasoningBlock,
		}
	}
	return items
}

// canAppend reports whether the conversation view is eligible for the
// incremental fast path: only new items at the tail, nothing mutated in
// place, same width, no thinking-indicator line to reconcile.
func (t *App) canAppend(w, n int) bool {
	return !t.needsFullRender.Load() &&
		t.renderedWidth == w &&
		n > t.renderedItems &&
		t.renderedItems > 0
}

// refreshMessages updates the conversation text view. When only new items
// were appended to the log since the last render, it formats just those and
// writes them incrementally; otherwise it rebuilds the full buffer. This
// keeps per-refresh work O(new content) during long streaming sessions
// instead of O(entire conversation). If the user has not scrolled up
// (t.userScrolled is false), it auto-scrolls to the end; otherwise it
// preserves the current viewport position.
func (t *App) refreshMessages() {
	start := time.Now()
	w := messageWidth(t.Messages)
	n := len(t.conversationLog)

	appended := false
	var formatDur, writeDur time.Duration
	var msgLen int

	if t.canAppend(w, n) {
		formatStart := time.Now()
		chunk := messages.Format(t.buildItems(t.renderedItems, n, w), messages.Content{
			Width: w,
			Theme: t.Theme,
		})
		formatDur = time.Since(formatStart)

		writeStart := time.Now()
		io.WriteString(t.Messages, chunk)
		writeDur = time.Since(writeStart)

		t.renderedItems = n
		msgLen = len(chunk)
		appended = true
	} else {
		items := t.buildItems(0, n, w)

		formatStart := time.Now()
		msg := messages.Format(items, messages.Content{
			Width: w,
			Theme: t.Theme,
		})
		formatDur = time.Since(formatStart)

		writeStart := time.Now()
		t.Messages.SetText(msg)
		writeDur = time.Since(writeStart)

		t.renderedItems = n
		t.renderedWidth = w
		t.needsFullRender.Store(false)
		msgLen = len(msg)
	}

	t.charsRendered.Store(int64(msgLen))

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

	_, span := otel.Tracer("yaah").Start(context.Background(), "tui.refresh",
		trace.WithAttributes(
			attribute.Int("items", n),
			attribute.Int("msg_bytes", msgLen),
			attribute.Bool("appended", appended),
			attribute.Int64("dur_total_us", totalDur.Microseconds()),
			attribute.Int64("dur_format_us", formatDur.Microseconds()),
			attribute.Int64("dur_settext_us", writeDur.Microseconds()),
			attribute.Int("queue_depth", queueDepth),
			attribute.Int64("ui_event_drops", t.uiEventDrops.Load()),
			attribute.Int64("ui_event_fallbacks", t.uiEventFallbacks.Load()),
			attribute.Int64("ui_event_fallback_saturated", t.uiEventFallbackSat.Load()),
		))
	span.End()
	observability.RecordTUIRefresh(context.Background(), totalDur, cadence, queueDepth)

	if totalDur > 50*time.Millisecond {
		log.Printf("SLOW refreshMessages: %d items, %d bytes, appended=%v total=%v format=%v write=%v",
			n, msgLen, appended, totalDur, formatDur, writeDur)
	}
}
