package tui

import (
	"context"
	"io"
	"log"
	"sort"
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
			ReasBlock: ci.reasoningBlock,
		}
	}
	return items
}

// canAppend reports whether the conversation view is eligible for the
// incremental fast path: only new items at the tail, nothing mutated in
// place, same width, no thinking-indicator line to reconcile.
//
// It says nothing about the chunk itself; refreshMessages additionally
// refuses chunks that begin with "\n" (see there for why).
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

	// tview's TextView keeps an incremental line index: after a Draw it
	// resumes parsing from the last indexed line when more bytes arrive
	// via Write. A resumption whose very first cluster is a "\n" is
	// mis-parsed at that boundary — the blank line between the previous
	// item and the new text collapses into a single leading space on the
	// response's first row. Newlines that are internal to the appended
	// batch parse fine (verified against a simulated screen), so
	// eligibility is decided from the first new item's kind: tool-line
	// chunks start with content and keep the O(new) append path, while
	// text and reasoning chunks begin with "\n" (messages.Format) and
	// take the full-render path, which resets the index via SetText.
	// Checking the kind before formatting also avoids formatting a tail
	// batch only to discard it on fallback.
	if t.canAppend(w, n) && t.conversationLog[t.renderedItems].toolBlock != nil {
		formatStart := time.Now()
		chunk := messages.Format(t.buildItems(t.renderedItems, n, w), messages.Content{
			Width: w,
			Theme: t.Theme,
		})
		formatDur = time.Since(formatStart)

		writeStart := time.Now()
		io.WriteString(t.Messages, chunk)
		writeDur = time.Since(writeStart)

		t.extendPromptAnchors(t.renderedItems, n, w)
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

		t.fullRenderCount++
		t.renderedItems = n
		t.renderedWidth = w
		t.needsFullRender.Store(false)
		msgLen = len(msg)

		t.rebuildPromptAnchors(items, w)
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

// itemRows returns the item's display-row count at width w, cached on
// the convItem. In-place block mutations clear the cache; width changes
// remeasure wholesale (rowsWidth mismatch).
func (t *App) itemRows(ci *convItem, it messages.Item, w int) int {
	if ci.rowsWidth == w {
		return ci.rowsCache
	}
	rows := messages.ItemRows(it, messages.Content{Width: w, Theme: t.Theme})
	ci.rowsCache = rows
	ci.rowsWidth = w
	return rows
}

// rebuildPromptAnchors recomputes the anchor table from scratch during a
// full render. Row estimates use the same measure Format's emissions are
// checked against (itemRows), so counted and painted rows share one
// code path; the per-item cache keeps the walk itself cheap.
func (t *App) rebuildPromptAnchors(items []messages.Item, w int) {
	t.promptAnchors = t.promptAnchors[:0]
	rows := 0
	for i := range t.conversationLog {
		ci := &t.conversationLog[i]
		if ci.isPrompt {
			t.promptAnchors = append(t.promptAnchors, promptAnchor{
				item:      i,
				startLine: rows + 1, // the leading "\n" separator row
				input:     ci.prompt,
			})
		}
		rows += t.itemRows(ci, items[i], w)
	}
	t.renderedRows = rows
}

// extendPromptAnchors delta-appends anchors for newly rendered items on
// the incremental path, continuing from the rows already accounted for.
func (t *App) extendPromptAnchors(from, to, w int) {
	for i := from; i < to; i++ {
		ci := &t.conversationLog[i]
		if ci.isPrompt {
			t.promptAnchors = append(t.promptAnchors, promptAnchor{
				item:      i,
				startLine: t.renderedRows + 1,
				input:     ci.prompt,
			})
		}
		t.renderedRows += t.itemRows(ci, t.buildItems(i, i+1, w)[0], w)
	}
}

// reconcilePromptPin re-derives the sticky header from the scroll
// position: it shows the most recent prompt whose first row is at or
// above the current viewport top. That single rule produces the whole
// behavior — a submitted question does not take over the pin until
// auto-scroll or scrolling carries it across the top seam, the previous
// prompt stays pinned until then, and scrolling back up de-pins the
// current question and re-pins the one above it. Runs after every app
// draw; an unchanged pin costs one binary search.
func (t *App) reconcilePromptPin() {
	want := ""
	if n := len(t.promptAnchors); n > 0 {
		row, _ := t.Messages.GetScrollOffset()
		i := sort.Search(n, func(i int) bool { return t.promptAnchors[i].startLine > row })
		if i > 0 {
			want = t.promptAnchors[i-1].input
		}
	}
	if want == t.pinnedPrompt {
		return
	}
	t.pinnedPrompt = want
	if want == "" {
		t.promptEcho.SetText("")
		t.messagesCol.ResizeItem(t.promptEcho, 0, 0)
		return
	}
	t.SetCurrentPrompt(want)
}
