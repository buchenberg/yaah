package tui

import (
	"context"
	"time"

	"github.com/buchenberg/yaah/internal/observability"
)

const (
	uiEventQueueSize         = 512
	uiEventCriticalWaitLimit = 10 * time.Millisecond

	// uiMaxDirectFallbacks caps goroutines spawned by enqueueUIEventDirect.
	// tview's QueueUpdate/QueueUpdateDraw block until the main loop services
	// them, so uncapped spawning piles up blocked goroutines whenever the UI
	// thread stalls, and any straggler after App.Run returns blocks forever.
	uiMaxDirectFallbacks = 8
)

type uiEvent struct {
	draw bool
	fn   func()
}

// startUIEventLoop starts a single consumer that serializes UI updates.
func (t *App) startUIEventLoop(done <-chan struct{}) {
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

func (t *App) queueUpdate(fn func()) {
	t.enqueueUIEvent(false, fn, false)
}

func (t *App) queueUpdateDraw(fn func()) {
	t.enqueueUIEvent(true, fn, false)
}

func (t *App) queueUpdateCritical(fn func()) {
	t.enqueueUIEvent(false, fn, true)
}

func (t *App) queueUpdateDrawCritical(fn func()) {
	t.enqueueUIEvent(true, fn, true)
}

func (t *App) queueThinkingUpdate(text string) {
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

func (t *App) runThinkingUpdate() {
	t.coalesceMu.Lock()
	label := t.pendingThinkingLabel
	seq := t.thinkingSeq
	t.coalesceMu.Unlock()

	if !t.thinkingInd.Visible() {
		t.thinkingInd.Show()
		t.needsFullRender.Store(true)
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

func (t *App) queueContextInfoUpdate(tokens, window int) {
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

func (t *App) runContextInfoUpdate() {
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
func (t *App) enqueueUIEvent(draw bool, fn func(), critical bool) {
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

// enqueueUIEventDirect runs an event outside the managed queue. It is the
// critical-path fallback when the queue is saturated, or the path before
// Run() starts the consumer. Concurrency is capped by uiMaxDirectFallbacks
// (see the const comment) and in-flight work is abandoned at shutdown so
// goroutines never block forever on a stopped application.
func (t *App) enqueueUIEventDirect(draw bool, fn func()) {
	t.bgMu.Lock()
	done := t.bgDone
	t.bgMu.Unlock()

	timer := time.NewTimer(uiEventCriticalWaitLimit)
	defer timer.Stop()
	select {
	case t.fallbackSem <- struct{}{}:
	case <-done:
		return
	case <-timer.C:
		t.uiEventFallbackSat.Add(1)
		observability.RecordTUIQueueEvent(context.Background(), "fallback", "saturated", -1)
		return
	}

	go func() {
		defer func() { <-t.fallbackSem }()

		// Best-effort shutdown check: shrinks the window in which a
		// straggler blocks forever on QueueUpdate after App.Run returns.
		if done != nil {
			select {
			case <-done:
				return
			default:
			}
		}
		if draw {
			t.App.QueueUpdateDraw(fn)
		} else {
			t.App.QueueUpdate(fn)
		}
	}()
}

func (t *App) uiQueueDepth() int {
	t.bgMu.Lock()
	defer t.bgMu.Unlock()
	if t.uiEventCh == nil {
		return -1
	}
	return len(t.uiEventCh)
}
