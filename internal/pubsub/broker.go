package pubsub

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMustDeliverTimeout = 50 * time.Millisecond
	defaultBufferSize         = 4096
)

// subscriber is one broker subscription. Subscriptions are held by pointer
// so a cancel handle can detach exactly its own registration. detached
// reports whether ch has been closed, and is guarded by Broker.mu.
type subscriber[T any] struct {
	id       string
	ch       chan T
	detached bool
}

type Broker[T any] struct {
	mu                 sync.RWMutex
	subs               []*subscriber[T]
	mustDeliverTimeout time.Duration
	dropped            atomic.Int64
	delivered          atomic.Int64
	closed             atomic.Bool
}

func NewBroker[T any]() *Broker[T] {
	return &Broker[T]{
		mustDeliverTimeout: defaultMustDeliverTimeout,
	}
}

func (b *Broker[T]) Publish(event T) {
	if b.closed.Load() {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subs {
		select {
		case s.ch <- event:
			b.delivered.Add(1)
		default:
			b.dropped.Add(1)
		}
	}
}

// PublishMustDeliver delivers the event to every subscriber, waiting up to
// mustDeliverTimeout for the fan-out as a whole.
//
// The timeout is a single total budget shared by all subscribers, not a
// per-subscriber allowance: a stalled subscriber consumes the budget and
// every later one is counted as dropped without a send attempt, so the
// call is bounded by mustDeliverTimeout regardless of subscriber count.
// The delivered/dropped accounting is unchanged — each subscriber is
// counted exactly once.
//
// It holds the read lock for the duration of delivery on purpose: the
// write-lock holders (Close, Unsubscribe) close subscriber channels, and
// the read lock guarantees no channel is closed while a send is in
// flight. Publishers use the read lock too, so concurrent Publish and
// PublishMustDeliver calls never block each other — only teardown waits
// for in-flight delivery (bounded by mustDeliverTimeout in total).
func (b *Broker[T]) PublishMustDeliver(event T) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed.Load() {
		return
	}
	b.deliverMust(event)
}

// TryPublishMustDeliver is PublishMustDeliver with the open check performed
// under the read lock. It reports false when the broker is already closed or
// has no subscribers, letting callers fall back to another delivery path
// instead of silently dropping the event during teardown. It shares
// PublishMustDeliver's single deadline for the whole fan-out.
func (b *Broker[T]) TryPublishMustDeliver(event T) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed.Load() || len(b.subs) == 0 {
		return false
	}
	b.deliverMust(event)
	return true
}

// deliverMust fans event out to every current subscriber under one shared
// deadline. The caller holds the read lock and has verified the broker is
// open. Once the deadline fires the remaining subscribers are counted as
// dropped without a send attempt, so delivered+dropped still accounts for
// every subscriber.
func (b *Broker[T]) deliverMust(event T) {
	timer := time.NewTimer(b.mustDeliverTimeout)
	defer timer.Stop()
	expired := false
	for _, s := range b.subs {
		if expired {
			b.dropped.Add(1)
			continue
		}
		select {
		case s.ch <- event:
			b.delivered.Add(1)
		case <-timer.C:
			// The single fan-out budget is spent; every remaining
			// subscriber is counted dropped without a send attempt.
			b.dropped.Add(1)
			expired = true
		}
	}
}

// Subscribe registers a subscriber carrying the given id and returns its
// channel. Duplicate ids are allowed; the id is kept for diagnostics and is
// what Unsubscribe matches on. Use SubscribeHandle when the caller needs to
// detach exactly its own registration.
func (b *Broker[T]) Subscribe(id string, bufSize int) <-chan T {
	ch, _ := b.subscribe(id, bufSize)
	return ch
}

// SubscribeHandle registers an anonymous subscription and returns its
// channel together with a cancel handle. The handle detaches exactly this
// subscription — never another one that happens to share an id — by
// pointer identity, closing its channel. It is idempotent and safe to call
// after Close, which detaches every subscriber itself.
func (b *Broker[T]) SubscribeHandle(bufSize int) (<-chan T, func()) {
	return b.subscribe("", bufSize)
}

func (b *Broker[T]) subscribe(id string, bufSize int) (<-chan T, func()) {
	if bufSize <= 0 {
		bufSize = defaultBufferSize
	}
	ch := make(chan T, bufSize)
	b.mu.Lock()
	defer b.mu.Unlock()
	// Subscribing after Close (yaah-8jou.2): return an already-closed
	// channel instead of appending a subscription that Close will
	// never drain or close. Consumers ranging over it (e.g. the
	// BrokerView forwarder) exit immediately rather than leaking.
	if b.closed.Load() {
		close(ch)
		return ch, func() {}
	}
	s := &subscriber[T]{id: id, ch: ch}
	b.subs = append(b.subs, s)
	return ch, func() { b.detach(s) }
}

// detach removes s from the subscriber list by pointer identity and closes
// its channel. It is a no-op once s is detached, whether by an earlier
// cancel, by Unsubscribe, or by Close.
func (b *Broker[T]) detach(s *subscriber[T]) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.detached {
		return
	}
	for i, cur := range b.subs {
		if cur == s {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			break
		}
	}
	s.detached = true
	close(s.ch)
}

// Unsubscribe removes every subscription that carries the given id, closing
// each one. An empty id matches nothing: subscriptions made by
// SubscribeHandle are anonymous and are removed only by their cancel handle.
func (b *Broker[T]) Unsubscribe(id string) {
	if id == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.subs[:0]
	for _, s := range b.subs {
		if s.id != id {
			kept = append(kept, s)
			continue
		}
		if !s.detached {
			s.detached = true
			close(s.ch)
		}
	}
	b.subs = kept
}

func (b *Broker[T]) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
		s.detached = true
		close(s.ch)
	}
	b.subs = nil
}

func (b *Broker[T]) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

func (b *Broker[T]) Dropped() int64 {
	return b.dropped.Load()
}

func (b *Broker[T]) Delivered() int64 {
	return b.delivered.Load()
}
