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
// cancel handle instead of relying on a shared "view" id. The handle is not a
// teardown boundary: Close detaches this view's subscription and then retires
// the whole broker generation it belongs to, which is the loop's to retire —
// see Close for the ownership decision.
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
// broker, and waits for the forwarder goroutine to exit. The unsub call is
// what closes this view's channel; broker.Close is what ends the Run's event
// generation. Safe to call multiple times.
//
// Teardown ownership is settled (yaah-8jou.7): the LOOP owns the broker's
// lifetime, and closing the whole broker here rather than only this view's
// subscription is the intended contract, not an oversight left behind by the
// days when the view subscribed under a shared id. Three things rest on it:
//
//   - One broker per Run. applyDefaults re-arms a fresh broker whenever a Run
//     starts with no broker or with l.brokerClosed set, so each Run gets its
//     own event generation. That is what stops one Run's events, or a whole
//     buffered backlog, from surfacing in the next.
//   - The re-arm gate is built on these semantics. At the end of every Run,
//     publishDone delivers the DoneEvent with PublishMustDeliver, calls this
//     Close, and marks l.brokerClosed. The broker's own closed flag is then
//     what makes every later Publish/PublishMustDeliver on that generation a
//     no-op, what makes TryPublishMustDeliver report false so callers fall
//     back to another path, and what makes a late Subscribe hand back an
//     already-closed channel instead of registering a consumer nothing will
//     ever drain.
//   - Production has exactly one view per broker — the loop's brokerView, made
//     with it in applyDefaults — so detaching the view and retiring the
//     generation are the same event today. There is no second subscriber a
//     broker-wide close could disturb.
//
// Decoupling Close into unsubscribe-only would therefore be a redesign, not a
// one-line change: it needs a way of saying "this generation is finished" that
// is not itself a view action — a subscriber refcount, or an explicit retire
// call owned by publishDone — plus a decision about who sets the broker's
// closed flag. Left as "unsubscribe only", the retired broker would stay open
// with no consumers, a stale publisher would keep buffering into it rather
// than being refused, and applyDefaults would need a different signal to
// re-arm. Revisit if several concurrent views on one broker ever become real.
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
