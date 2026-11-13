package agent

import (
	"context"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/pubsub"
	"github.com/buchenberg/yaah/internal/types"
)

// Must-deliver classification (yaah-8jou.6). The loop publishes rare,
// must-not-lose events through PublishMustDeliver, which waits for a
// lagging subscriber instead of dropping the event the way Publish does on a
// full buffer. These tests pin that classification at the two surfaces where
// a drop is unrecoverable for the run: the once-per-session title and the
// paired compaction events.

// slowSubscriber builds a broker whose single subscriber already has its
// one-slot buffer occupied. The occupied slot is what separates the two
// publish APIs: a lossy Publish gives up immediately, a must-deliver one
// waits for the reader inside the broker's fan-out budget.
func slowSubscriber(t *testing.T) (*pubsub.Broker[Event], <-chan Event) {
	t.Helper()
	broker := pubsub.NewBroker[Event]()
	t.Cleanup(broker.Close)
	sub := broker.Subscribe("slow", 1)
	broker.Publish(&TokenDeltaEvent{Text: "filler"})
	return broker, sub
}

// drainSlowSubscriber releases the subscriber's slot — the read that lets a
// blocked must-deliver fan-out complete — and returns the next event it
// carries.
func drainSlowSubscriber(t *testing.T, sub <-chan Event) Event {
	t.Helper()
	select {
	case <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("filler event never arrived")
	}
	select {
	case ev := <-sub:
		if ev == nil {
			t.Fatal("subscriber closed before the event under test")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event delivered after the subscriber was released")
		return nil
	}
}

// TestPublishTitleMustDeliver pins the title classification: the event fires
// at most once per untitled session, so a drop leaves every client naming
// that session nothing for the whole run.
func TestPublishTitleMustDeliver(t *testing.T) {
	broker, sub := slowSubscriber(t)
	l := &Loop{broker: broker}

	go l.publishTitle("fix the flaky test", 1700000000)
	// Give the publish time to reach the fan-out while the slot is still
	// full. A lossy Publish had already dropped the event by now;
	// must-deliver is still waiting for the reader within its budget.
	time.Sleep(30 * time.Millisecond)

	ev := drainSlowSubscriber(t, sub)
	title, ok := ev.(*TitleSetEvent)
	if !ok {
		t.Fatalf("event delivered to the slow subscriber = %T, want *TitleSetEvent", ev)
	}
	if title.Title != "fix the flaky test" || title.UpdatedAt != 1700000000 {
		t.Fatalf("TitleSetEvent = %+v, want the published title and timestamp", title)
	}
}

// TestCompactionEventsMustDeliver pins the start/end pairing: with a
// subscriber one event behind, a pass still reports both its opening and its
// terminal event, in that order.
func TestCompactionEventsMustDeliver(t *testing.T) {
	broker, sub := slowSubscriber(t)
	// An empty (thought-only) primary summary drives the chunked fallback,
	// a path that ends in a CompactionDoneEvent like every other.
	cm := compactionTestManager(&compactionRecordingProvider{
		responses: []*types.ChatResponse{thoughtOnlyResponse()},
	})
	cm.Broker = broker

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		cm.compactContext(context.Background(), 0.25)
	}()
	time.Sleep(30 * time.Millisecond)

	ev := drainSlowSubscriber(t, sub)
	start, ok := ev.(*CompactionStartedEvent)
	if !ok {
		t.Fatalf("first compaction event = %T, want *CompactionStartedEvent", ev)
	}
	if start.Reason == "" {
		t.Fatal("CompactionStartedEvent carries no reason")
	}

	doneEv := awaitCompactionDone(t, sub)
	if doneEv.Method == "" {
		t.Fatal("CompactionDoneEvent carries no method")
	}

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("compaction pass did not finish")
	}
}

// awaitCompactionDone drains the subscription until a CompactionDoneEvent
// arrives, skipping anything the pass emits in between.
func awaitCompactionDone(t *testing.T, sub <-chan Event) *CompactionDoneEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub:
			if e, ok := ev.(*CompactionDoneEvent); ok {
				return e
			}
		case <-deadline:
			t.Fatal("no CompactionDoneEvent within deadline")
			return nil
		}
	}
}
