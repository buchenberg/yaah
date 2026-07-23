package agent

import "github.com/buchenberg/yaah/internal/pubsub"

// View consumes agent events from the agent loop. Implementations include
// the TUI (internal/tui), the REPL terminal output (cmd/yaah/terminalView),
// and test recording views.
//
// HandleEvent is called sequentially from a dedicated forwarder goroutine.
// Implementations must be safe for use from a single goroutine; the agent
// loop does not call HandleEvent concurrently.
type View interface {
	HandleEvent(Event)
}

// NoopView discards all events. Use as a placeholder when a View is
// required but no rendering is needed (e.g. sub-agents, headless mode).
type NoopView struct{}

func (NoopView) HandleEvent(Event) {}

// BrokerView adapts a pubsub.Broker subscription into a View by running
// a forwarder goroutine that reads from the subscription channel and
// calls HandleEvent on the target view.
//
// Usage:
//
//	broker := pubsub.NewBroker[agent.Event]()
//	bv := agent.NewBrokerView(broker, myView)
//	defer bv.Close()
//	// ... publish to broker ...
type BrokerView struct {
	broker *pubsub.Broker[Event]
	sub    <-chan Event
	done   chan struct{}
}

// NewBrokerView creates a BrokerView, subscribes to the broker, and
// starts a forwarder goroutine that delivers events to target.HandleEvent.
// Call Close() to stop forwarding and release resources.
func NewBrokerView(broker *pubsub.Broker[Event], target View) *BrokerView {
	sub := broker.Subscribe("view", 256)
	bv := &BrokerView{
		broker: broker,
		sub:    sub,
		done:   make(chan struct{}),
	}
	go bv.forward(target)
	return bv
}

// forward reads events from the subscription channel and delivers them
// to the target view. It exits when the subscription channel is closed
// (which happens when the broker is closed).
func (bv *BrokerView) forward(target View) {
	defer close(bv.done)
	for evt := range bv.sub {
		target.HandleEvent(evt)
	}
}

// Close closes the underlying broker and waits for the forwarder
// goroutine to exit. Safe to call multiple times.
func (bv *BrokerView) Close() {
	bv.broker.Close()
	<-bv.done
}

// SubscriberCount returns the number of subscribers on the underlying broker.
func (bv *BrokerView) SubscriberCount() int {
	return bv.broker.SubscriberCount()
}
