package tui2

import (
	"context"
	"time"

	"github.com/buchenberg/yaah/internal/observability"
)

const (
	uiEventQueueSize         = 512
	uiEventCriticalWaitLimit = 10 * time.Millisecond
)

type uiEvent struct {
	draw bool
	fn   func()
}

// startUIEventLoop starts a single consumer that serializes UI updates.
func (t *TUI2) startUIEventLoop(done <-chan struct{}) {
	go func() {
		for {
			select {
			case <-done:
				return
			case ev := <-t.uiEventCh:
				if ev.fn == nil {
					continue
				}
				if ev.draw {
					t.App.QueueUpdateDraw(ev.fn)
				} else {
					t.App.QueueUpdate(ev.fn)
				}
			}
		}
	}()
}

func (t *TUI2) queueUpdate(fn func()) {
	t.enqueueUIEvent(false, fn, false)
}

func (t *TUI2) queueUpdateDraw(fn func()) {
	t.enqueueUIEvent(true, fn, false)
}

func (t *TUI2) queueUpdateCritical(fn func()) {
	t.enqueueUIEvent(false, fn, true)
}

func (t *TUI2) queueUpdateDrawCritical(fn func()) {
	t.enqueueUIEvent(true, fn, true)
}

func (t *TUI2) queueThinkingUpdate(text string) {
	t.coalesceMu.Lock()
	t.pendingThinkingLabel = text
	t.thinkingSeq++
	if t.thinkingQueued {
		t.coalesceMu.Unlock()
		observability.RecordTUIQueueEvent(context.Background(), "thinking", "coalesced", t.uiQueueDepth())
		return
	}
	t.thinkingQueued = true
	t.coalesceMu.Unlock()

	t.queueUpdateDraw(func() {
		t.runThinkingUpdate()
	})
}

func (t *TUI2) runThinkingUpdate() {
	t.coalesceMu.Lock()
	label := t.pendingThinkingLabel
	seq := t.thinkingSeq
	t.coalesceMu.Unlock()

	if !t.thinkingInd.Visible() {
		t.thinkingInd.Show()
	}
	t.thinkingLabel = label

	t.coalesceMu.Lock()
	if seq == t.thinkingSeq {
		t.thinkingQueued = false
		t.coalesceMu.Unlock()
		return
	}
	t.coalesceMu.Unlock()

	t.queueUpdateDraw(func() {
		t.runThinkingUpdate()
	})
}

func (t *TUI2) queueContextInfoUpdate(tokens, window int) {
	t.coalesceMu.Lock()
	t.pendingContextTokens = tokens
	t.pendingContextWindow = window
	t.contextSeq++
	if t.contextQueued {
		t.coalesceMu.Unlock()
		observability.RecordTUIQueueEvent(context.Background(), "context", "coalesced", t.uiQueueDepth())
		return
	}
	t.contextQueued = true
	t.coalesceMu.Unlock()

	t.queueUpdateDraw(func() {
		t.runContextInfoUpdate()
	})
}

func (t *TUI2) runContextInfoUpdate() {
	t.coalesceMu.Lock()
	tokens := t.pendingContextTokens
	window := t.pendingContextWindow
	seq := t.contextSeq
	t.coalesceMu.Unlock()

	t.contextTokens = tokens
	t.contextWindow = window
	t.renderInfoPane()

	t.coalesceMu.Lock()
	if seq == t.contextSeq {
		t.contextQueued = false
		t.coalesceMu.Unlock()
		return
	}
	t.coalesceMu.Unlock()

	t.queueUpdateDraw(func() {
		t.runContextInfoUpdate()
	})
}

// enqueueUIEvent sends event work into a bounded queue. Non-critical events
// are dropped when full. Critical events wait briefly, then fall back to
// direct async QueueUpdate/QueueUpdateDraw so they are not dropped.
func (t *TUI2) enqueueUIEvent(draw bool, fn func(), critical bool) {
	if fn == nil {
		return
	}

	t.bgMu.Lock()
	ch := t.uiEventCh
	done := t.bgDone
	depth := 0
	if ch != nil {
		depth = len(ch)
	}
	t.bgMu.Unlock()

	if ch == nil {
		observability.RecordTUIQueueEvent(context.Background(), "direct", "fallback", -1)
		t.enqueueUIEventDirect(draw, fn)
		return
	}

	eventType := "update"
	if draw {
		eventType = "draw"
	}

	ev := uiEvent{draw: draw, fn: fn}
	select {
	case ch <- ev:
		observability.RecordTUIQueueEvent(context.Background(), eventType, "enqueued", depth)
		return
	default:
	}

	if !critical {
		t.uiEventDrops.Add(1)
		observability.RecordTUIQueueEvent(context.Background(), eventType, "dropped", depth)
		return
	}

	timer := time.NewTimer(uiEventCriticalWaitLimit)
	defer timer.Stop()
	select {
	case ch <- ev:
		observability.RecordTUIQueueEvent(context.Background(), eventType, "enqueued", depth)
		return
	case <-done:
		return
	case <-timer.C:
		t.uiEventFallbacks.Add(1)
		observability.RecordTUIQueueEvent(context.Background(), eventType, "fallback", depth)
		t.enqueueUIEventDirect(draw, fn)
	}
}

func (t *TUI2) enqueueUIEventDirect(draw bool, fn func()) {
	if draw {
		go t.App.QueueUpdateDraw(fn)
	} else {
		go t.App.QueueUpdate(fn)
	}
}

func (t *TUI2) uiQueueDepth() int {
	t.bgMu.Lock()
	defer t.bgMu.Unlock()
	if t.uiEventCh == nil {
		return -1
	}
	return len(t.uiEventCh)
}
