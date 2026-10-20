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

// A view that cannot safely write between runs (REPL/ACP) must receive the
// event on the next RunPrompt rather than from the job goroutine.
func TestDispatchEventBuffersForUnsafeView(t *testing.T) {
	sess := &agentSession{}
	view := &captureView{}
	sess.SetView(view)

	sess.dispatchEvent(&agent.SubAgentEndEvent{SubAgentID: "bg-1"})
	if len(view.events) != 0 {
		t.Fatalf("unsafe view received %d event(s) between runs, want 0", len(view.events))
	}

	pending := sess.takePendingEvents()
	if len(pending) != 1 {
		t.Fatalf("pending events = %d, want 1", len(pending))
	}
	if _, ok := pending[0].(*agent.SubAgentEndEvent); !ok {
		t.Fatalf("pending event = %T, want *agent.SubAgentEndEvent", pending[0])
	}
	if got := sess.takePendingEvents(); len(got) != 0 {
		t.Errorf("second drain returned %d event(s), want 0", len(got))
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
	if got := sess.takePendingEvents(); len(got) != 0 {
		t.Errorf("safe view buffered %d event(s), want 0", len(got))
	}
}

// Headless/serve sessions have no view; buffering their events would retain
// results forever.
func TestDispatchEventDropsWithoutView(t *testing.T) {
	sess := &agentSession{}
	sess.dispatchEvent(&agent.SubAgentEndEvent{SubAgentID: "bg-1"})
	if got := sess.takePendingEvents(); len(got) != 0 {
		t.Fatalf("no-view session buffered %d event(s), want 0", len(got))
	}

	sess.SetView(agent.NoopView{})
	sess.dispatchEvent(&agent.SubAgentEndEvent{SubAgentID: "bg-2"})
	if got := sess.takePendingEvents(); len(got) != 0 {
		t.Fatalf("NoopView session buffered %d event(s), want 0", len(got))
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
	if got := len(sess.takePendingEvents()); got != maxPendingSessionEvents {
		t.Fatalf("pending buffer = %d, want %d", got, maxPendingSessionEvents)
	}
}
