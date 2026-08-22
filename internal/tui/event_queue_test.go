package tui

import "testing"

func TestQueueUpdate_DropsNonCriticalWhenFull(t *testing.T) {
	ui := New("test")
	ui.bgDone = make(chan struct{})
	ui.uiEventCh = make(chan uiEvent, 1)
	ui.uiEventCh <- uiEvent{fn: func() {}}

	ui.queueUpdate(func() {})

	if got := ui.uiEventDrops.Load(); got != 1 {
		t.Fatalf("expected 1 dropped event, got %d", got)
	}
}

func TestQueueUpdate_CriticalFallbackWhenFull(t *testing.T) {
	ui := New("test")
	ui.bgDone = make(chan struct{})
	ui.uiEventCh = make(chan uiEvent, 1)
	ui.uiEventCh <- uiEvent{fn: func() {}}

	ui.queueUpdateCritical(func() {})

	if got := ui.uiEventFallbacks.Load(); got != 1 {
		t.Fatalf("expected 1 fallback event, got %d", got)
	}
}

func TestQueueThinkingUpdate_CoalescesToLatest(t *testing.T) {
	ui := New("test")
	ui.bgDone = make(chan struct{})
	ui.uiEventCh = make(chan uiEvent, 8)

	ui.queueThinkingUpdate("one")
	ui.queueThinkingUpdate("two")
	ui.queueThinkingUpdate("three")

	if got := len(ui.uiEventCh); got != 1 {
		t.Fatalf("expected 1 queued thinking event, got %d", got)
	}
	if ui.pendingThinkingLabel != "three" {
		t.Fatalf("expected latest thinking label, got %q", ui.pendingThinkingLabel)
	}
}

func TestQueueContextInfoUpdate_CoalescesToLatest(t *testing.T) {
	ui := New("test")
	ui.bgDone = make(chan struct{})
	ui.uiEventCh = make(chan uiEvent, 8)

	ui.queueContextInfoUpdate(10, 100)
	ui.queueContextInfoUpdate(20, 200)
	ui.queueContextInfoUpdate(30, 300)

	if got := len(ui.uiEventCh); got != 1 {
		t.Fatalf("expected 1 queued context event, got %d", got)
	}
	if ui.pendingContextTokens != 30 || ui.pendingContextWindow != 300 {
		t.Fatalf("expected latest context snapshot, got tokens=%d window=%d", ui.pendingContextTokens, ui.pendingContextWindow)
	}
}

func TestQueueUpdate_PreservesFIFOOrder(t *testing.T) {
	ui := New("test")
	ui.bgDone = make(chan struct{})
	ui.uiEventCh = make(chan uiEvent, 4)

	seq := []int{}
	ui.queueUpdateCritical(func() { seq = append(seq, 1) })
	ui.queueUpdateCritical(func() { seq = append(seq, 2) })

	if got := len(ui.uiEventCh); got != 2 {
		t.Fatalf("expected 2 queued events, got %d", got)
	}

	ev1 := <-ui.uiEventCh
	ev2 := <-ui.uiEventCh
	ev1.fn()
	ev2.fn()

	if len(seq) != 2 || seq[0] != 1 || seq[1] != 2 {
		t.Fatalf("unexpected execution order: %v", seq)
	}
}

func TestBackgroundLoops_StartStopLifecycle(t *testing.T) {
	ui := New("test")
	done := ui.startBackgroundLoops()
	if done == nil {
		t.Fatal("expected non-nil done channel")
	}
	if ui.uiEventCh == nil {
		t.Fatal("expected uiEventCh initialized")
	}

	ui.stopBackgroundLoops()
	if ui.bgDone != nil {
		t.Fatal("expected bgDone cleared on stop")
	}
	if ui.uiEventCh != nil {
		t.Fatal("expected uiEventCh cleared on stop")
	}
}
