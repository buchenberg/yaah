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
	unsub  func()
	done   chan struct{}
	sync   func()
}

// NewBrokerView creates a BrokerView, subscribes to the broker, and
// starts a forwarder goroutine that delivers events to target.HandleEvent.
// Call Close() to stop forwarding and release resources.
func NewBrokerView(broker *pubsub.Broker[Event], target View) *BrokerView {
	return NewBrokerViewWithSync(broker, target, nil)
}

// NewBrokerViewWithSync is NewBrokerView with a sync callback that runs on
// the forwarder goroutine before the first broker event is delivered. A
// session uses it to replay events buffered before this run's broker existed,
// preserving their order relative to live broker events.
//
// The subscription is taken through SubscribeHandle so the view holds its own
// cancel handle instead of relying on a shared "view" id. Close still closes
// the whole broker; which side owns teardown is yaah-8jou.7.
func NewBrokerViewWithSync(broker *pubsub.Broker[Event], target View, syncFn func()) *BrokerView {
	sub, unsub := broker.SubscribeHandle(4096)
	bv := &BrokerView{
		broker: broker,
		sub:    sub,
		unsub:  unsub,
		done:   make(chan struct{}),
		sync:   syncFn,
	}
	go bv.forward(target)
	return bv
}

// forward reads events from the subscription channel and delivers them
// to the target view. It exits when the subscription channel is closed
// (by the view's own cancel handle or by the broker's Close).
func (bv *BrokerView) forward(target View) {
	defer close(bv.done)
	if bv.sync != nil {
		bv.sync()
	}
	for evt := range bv.sub {
		target.HandleEvent(evt)
	}
}

// Close detaches the view's own subscription, closes the underlying
// broker, and waits for the forwarder goroutine to exit. The unsub call
// is what closes this view's channel; broker.Close still runs so the
// lifecycle re-arm gate (brokerClosed) keeps its meaning until yaah-8jou.7
// decides teardown ownership. Safe to call multiple times.
func (bv *BrokerView) Close() {
	if bv.unsub != nil {
		bv.unsub()
	}
	bv.broker.Close()
	<-bv.done
}

// SubscriberCount returns the number of subscribers on the underlying broker.
func (bv *BrokerView) SubscriberCount() int {
	return bv.broker.SubscriberCount()
}
