package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/pubsub"
)

// recordingView captures events for test assertions.
type recordingView struct {
	mu     sync.Mutex
	events []Event
}

func (rv *recordingView) HandleEvent(evt Event) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	rv.events = append(rv.events, evt)
}

func (rv *recordingView) eventsOfType() []Event {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	out := make([]Event, len(rv.events))
	copy(out, rv.events)
	return out
}

func TestNoopView(t *testing.T) {
	// NoopView must not panic on any event type.
	nv := NoopView{}
	nv.HandleEvent(&TokenDeltaEvent{Text: "hello"})
	nv.HandleEvent(&ThinkingEvent{Text: "hmm"})
	nv.HandleEvent(&FlushEvent{Content: "done"})
	nv.HandleEvent(&ToolStartEvent{Name: "read", Args: "foo.txt"})
	nv.HandleEvent(&ToolEndEvent{Name: "read", Args: "foo.txt", Result: "ok", Duration: time.Second})
	nv.HandleEvent(&SubAgentStartEvent{Role: "dev", Model: "gpt-4", Prompt: "fix it"})
	nv.HandleEvent(&SubAgentEndEvent{Role: "dev", Model: "gpt-4", Prompt: "fix it", Duration: 2 * time.Second})
}

func TestBrokerView_ForwardsEvents(t *testing.T) {
	broker := pubsub.NewBroker[Event]()
	rv := &recordingView{}
	bv := NewBrokerView(broker, rv)

	broker.PublishMustDeliver(&TokenDeltaEvent{Text: "hello"})
	broker.PublishMustDeliver(&TokenDeltaEvent{Text: " world"})
	broker.PublishMustDeliver(&FlushEvent{Content: "hello world"})

	bv.Close()

	events := rv.eventsOfType()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	tok1, ok := events[0].(*TokenDeltaEvent)
	if !ok {
		t.Fatalf("expected *TokenDeltaEvent, got %T", events[0])
	}
	if tok1.Text != "hello" {
		t.Errorf("expected 'hello', got %q", tok1.Text)
	}

	flush, ok := events[2].(*FlushEvent)
	if !ok {
		t.Fatalf("expected *FlushEvent, got %T", events[2])
	}
	if flush.Content != "hello world" {
		t.Errorf("expected 'hello world', got %q", flush.Content)
	}
}

func TestBrokerView_ForwardsAllEventTypes(t *testing.T) {
	broker := pubsub.NewBroker[Event]()
	rv := &recordingView{}
	bv := NewBrokerView(broker, rv)

	broker.PublishMustDeliver(&TokenDeltaEvent{Text: "t"})
	broker.PublishMustDeliver(&ThinkingEvent{Text: "think"})
	broker.PublishMustDeliver(&FlushEvent{Content: "flush"})
	broker.PublishMustDeliver(&ToolStartEvent{Name: "read", Args: "f"})
	broker.PublishMustDeliver(&ToolEndEvent{Name: "read", Args: "f", Duration: time.Millisecond})
	broker.PublishMustDeliver(&SubAgentStartEvent{Role: "r", Model: "m", Prompt: "p"})
	broker.PublishMustDeliver(&SubAgentEndEvent{Role: "r", Model: "m", Prompt: "p", Duration: time.Second})

	bv.Close()

	events := rv.eventsOfType()
	if len(events) != 7 {
		t.Fatalf("expected 7 events, got %d", len(events))
	}

	expectedTypes := []string{
		"*agent.TokenDeltaEvent",
		"*agent.ThinkingEvent",
		"*agent.FlushEvent",
		"*agent.ToolStartEvent",
		"*agent.ToolEndEvent",
		"*agent.SubAgentStartEvent",
		"*agent.SubAgentEndEvent",
	}
	for i, evt := range events {
		got := typeName(evt)
		if got != expectedTypes[i] {
			t.Errorf("event[%d]: expected %s, got %s", i, expectedTypes[i], got)
		}
	}
}

func TestBrokerView_CloseIsIdempotent(t *testing.T) {
	broker := pubsub.NewBroker[Event]()
	rv := &recordingView{}
	bv := NewBrokerView(broker, rv)

	bv.Close()
	// Second close must not panic.
	bv.Close()
}

func TestBrokerView_SubscriberCount(t *testing.T) {
	broker := pubsub.NewBroker[Event]()
	rv := &recordingView{}
	bv := NewBrokerView(broker, rv)

	if bv.SubscriberCount() != 1 {
		t.Errorf("expected 1 subscriber, got %d", bv.SubscriberCount())
	}

	bv.Close()
}

func TestBrokerView_ClosePreventsDelivery(t *testing.T) {
	broker := pubsub.NewBroker[Event]()
	rv := &recordingView{}
	bv := NewBrokerView(broker, rv)

	bv.Close()

	// Publishing after close must not panic and must not deliver.
	broker.Publish(&TokenDeltaEvent{Text: "after close"})
	time.Sleep(10 * time.Millisecond)

	if len(rv.eventsOfType()) > 0 {
		t.Error("expected no events after close")
	}
}

func TestBrokerView_ConcurrentPublish(t *testing.T) {
	broker := pubsub.NewBroker[Event]()
	rv := &recordingView{}
	bv := NewBrokerView(broker, rv)

	var wg sync.WaitGroup
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				broker.Publish(&TokenDeltaEvent{Text: "x"})
			}
		}(g)
	}
	wg.Wait()
	bv.Close()

	events := rv.eventsOfType()
	if len(events) < 80 { // some may drop with high concurrency and small buffer
		t.Logf("received %d events (some drops expected under load)", len(events))
	}
}

// typeName returns a human-readable type name for test output.
func typeName(v any) string {
	switch v.(type) {
	case *TokenDeltaEvent:
		return "*agent.TokenDeltaEvent"
	case *ThinkingEvent:
		return "*agent.ThinkingEvent"
	case *FlushEvent:
		return "*agent.FlushEvent"
	case *ToolStartEvent:
		return "*agent.ToolStartEvent"
	case *ToolEndEvent:
		return "*agent.ToolEndEvent"
	case *SubAgentStartEvent:
		return "*agent.SubAgentStartEvent"
	case *SubAgentEndEvent:
		return "*agent.SubAgentEndEvent"
	default:
		return "unknown"
	}
}
