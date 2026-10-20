package yaah

import (
	"testing"

	"github.com/buchenberg/yaah/internal/agent"
)

type captureView struct {
	events []agent.Event
}

func (v *captureView) HandleEvent(evt agent.Event) { v.events = append(v.events, evt) }

type safeCaptureView struct {
	captureView
}

func (v *safeCaptureView) AcceptsEventsBetweenRuns() bool { return true }

// A view that cannot safely write between runs (REPL/ACP) receives the event
// when the next run's view forwarder drains the buffer.
func TestDispatchEventBuffersForUnsafeView(t *testing.T) {
	sess := &agentSession{}
	view := &captureView{}
	sess.SetView(view)

	sess.dispatchEvent(&agent.SubAgentEndEvent{SubAgentID: "bg-1"})
	if len(view.events) != 0 {
		t.Fatalf("unsafe view received %d event(s) between runs, want 0", len(view.events))
	}

	sess.deliverPendingEvents()
	if len(view.events) != 1 {
		t.Fatalf("view received %d event(s) after drain, want 1", len(view.events))
	}
	if _, ok := view.events[0].(*agent.SubAgentEndEvent); !ok {
		t.Fatalf("delivered event = %T, want *agent.SubAgentEndEvent", view.events[0])
	}
	if len(sess.pendingEvents) != 0 {
		t.Errorf("pending buffer = %d after drain, want 0", len(sess.pendingEvents))
	}
}

// Views that marshal HandleEvent onto their own queue (TUI/web) receive
// between-run events immediately.
func TestDispatchEventDeliversForSafeView(t *testing.T) {
	sess := &agentSession{}
	view := &safeCaptureView{}
	sess.SetView(view)

	sess.dispatchEvent(&agent.SubAgentEndEvent{SubAgentID: "bg-1"})
	if len(view.events) != 1 {
		t.Fatalf("safe view received %d event(s), want 1", len(view.events))
	}
	if len(sess.pendingEvents) != 0 {
		t.Errorf("safe view buffered %d event(s), want 0", len(sess.pendingEvents))
	}
}

// Headless/serve sessions have no view; buffering their events would retain
// results forever.
func TestDispatchEventDropsWithoutView(t *testing.T) {
	sess := &agentSession{}
	sess.dispatchEvent(&agent.SubAgentEndEvent{SubAgentID: "bg-1"})
	if len(sess.pendingEvents) != 0 {
		t.Fatalf("no-view session buffered %d event(s), want 0", len(sess.pendingEvents))
	}

	sess.SetView(agent.NoopView{})
	sess.dispatchEvent(&agent.SubAgentEndEvent{SubAgentID: "bg-2"})
	if len(sess.pendingEvents) != 0 {
		t.Fatalf("NoopView session buffered %d event(s), want 0", len(sess.pendingEvents))
	}
}

// ACP installs a new view per prompt. Buffered events must replay to the
// view that produced them, not to whichever session arrives next.
func TestPendingEventsKeepTheirDestinationView(t *testing.T) {
	sess := &agentSession{}
	viewA := &captureView{}
	viewB := &captureView{}

	sess.SetView(viewA)
	sess.dispatchEvent(&agent.SubAgentEndEvent{SubAgentID: "bg-a"})

	sess.SetView(viewB)
	sess.deliverPendingEvents()

	if len(viewA.events) != 1 {
		t.Fatalf("view A received %d event(s), want 1", len(viewA.events))
	}
	if len(viewB.events) != 0 {
		t.Fatalf("view B received %d event(s), want 0", len(viewB.events))
	}
}

// An unsafe view that never starts another run must not grow the buffer
// without bound.
func TestDispatchEventBufferIsBounded(t *testing.T) {
	sess := &agentSession{}
	sess.SetView(&captureView{})

	for i := 0; i < maxPendingSessionEvents+10; i++ {
		sess.dispatchEvent(&agent.SubAgentEndEvent{SubAgentID: "bg"})
	}
	if got := len(sess.pendingEvents); got != maxPendingSessionEvents {
		t.Fatalf("pending buffer = %d, want %d", got, maxPendingSessionEvents)
	}
}

// Evicting a start must evict its matching end too, so the cap cannot leave
// a lone completion for the REPL/ACP to render.
func TestPendingEventEvictionDropsStartAndEndTogether(t *testing.T) {
	sess := &agentSession{}
	sess.SetView(&captureView{})

	sess.dispatchEvent(&agent.SubAgentStartEvent{SubAgentID: "bg-1"})
	for i := 0; i < maxPendingSessionEvents-1; i++ {
		sess.dispatchEvent(&agent.SubAgentStartEvent{SubAgentID: "bg-other"})
	}
	sess.dispatchEvent(&agent.SubAgentEndEvent{SubAgentID: "bg-1"})
	// Buffer is at the cap, with bg-1's start at the front and its end last.
	// The next dispatch evicts the start and must take the end with it.
	sess.dispatchEvent(&agent.SubAgentStartEvent{SubAgentID: "bg-next"})

	for _, p := range sess.pendingEvents {
		if end, ok := p.evt.(*agent.SubAgentEndEvent); ok && end.SubAgentID == "bg-1" {
			t.Fatal("evicted start left its matching end in the buffer")
		}
	}
}
