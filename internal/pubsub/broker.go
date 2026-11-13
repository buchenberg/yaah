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
//
// delivered/dropped are this subscription's own fan-out counters, bumped at
// every accounting point that moves the broker-wide aggregates (see
// countDelivered/countDropped). They exist so Stats can name a slow or wedged
// subscriber instead of collapsing every view into one global number, and
// they are deliberately a mirror rather than a second source of truth: the
// globals keep their exact meaning.
//
// Atomics are sufficient. Publishers and deliverMust update the counters under
// the broker's read lock, detach updates nothing, and a counter is never read
// to decide whether a send may happen — so Stats can take a value under the
// same read lock without tearing.
type subscriber[T any] struct {
	id        string
	ch        chan T
	detached  bool
	delivered atomic.Int64
	dropped   atomic.Int64
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

// countDelivered accounts one event accepted by s's channel on both the
// broker-wide aggregate and the subscription's own counter. Every delivery
// site in Publish and deliverMust goes through these two helpers, which is
// what keeps Stats() reconcilable with Delivered()/Dropped().
func (b *Broker[T]) countDelivered(s *subscriber[T]) {
	b.delivered.Add(1)
	s.delivered.Add(1)
}

// countDropped accounts one event that did not reach s's channel. See
// countDelivered for the dual-bookkeeping rule.
func (b *Broker[T]) countDropped(s *subscriber[T]) {
	b.dropped.Add(1)
	s.dropped.Add(1)
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
			b.countDelivered(s)
		default:
			b.countDropped(s)
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
			b.countDropped(s)
			continue
		}
		select {
		case s.ch <- event:
			b.countDelivered(s)
		case <-timer.C:
			// The single fan-out budget is spent; every remaining
			// subscriber is counted dropped without a send attempt.
			b.countDropped(s)
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

// Dropped returns the broker-wide count of events that did not reach a
// subscriber's channel since the broker was created. It is lifetime-wide and
// subscription-blind: counts recorded against a subscriber that has since been
// detached, unsubscribed or closed stay in the total, so it is not the sum of
// the rows Stats currently returns.
func (b *Broker[T]) Dropped() int64 {
	return b.dropped.Load()
}

// Delivered returns the broker-wide count of events accepted by a
// subscriber's channel since the broker was created. Accepted means buffered,
// not consumed: a wedged reader still counts as delivered until its buffer
// fills. Same lifetime-wide caveat as Dropped.
func (b *Broker[T]) Delivered() int64 {
	return b.delivered.Load()
}

// SubscriberStats is one row of Broker.Stats: a subscription's id together
// with its own delivered/dropped counters. Anonymous subscriptions, made
// through SubscribeHandle, carry an empty ID; their position in the slice is
// what tells two of them apart.
type SubscriberStats struct {
	ID        string
	Delivered int64
	Dropped   int64
}

// Stats snapshots the per-subscription counters in subscription order, under
// the same read lock the fan-out uses, and returns copies — mutating the
// result cannot move a broker counter.
//
// Counting semantics are a deliberate mirror of the globals, not a split by
// delivery mode: both the lossy Publish path and the must-deliver paths
// (PublishMustDeliver, TryPublishMustDeliver) bump the owning subscription's
// counter at exactly the sites that bump the aggregate. That keeps
// sum(Stats.Delivered)+sum(Stats.Dropped) reconcilable with
// Delivered()+Dropped() whenever no subscription has been detached, which is
// the property the per-Run diagnostic depends on. Once a subscription is
// detached its row disappears while its history stays in the globals, so the
// sums can fall below the aggregates as views come and go; read Stats for
// "which live view is slow" and the globals for lifetime totals.
//
// The returned slice is never nil, and is empty for a broker with no
// subscribers (including a closed one).
func (b *Broker[T]) Stats() []SubscriberStats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]SubscriberStats, 0, len(b.subs))
	for _, s := range b.subs {
		out = append(out, SubscriberStats{
			ID:        s.id,
			Delivered: s.delivered.Load(),
			Dropped:   s.dropped.Load(),
		})
	}
	return out
}
