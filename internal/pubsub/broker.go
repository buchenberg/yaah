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

type subscriber[T any] struct {
	id string
	ch chan T
}

type Broker[T any] struct {
	mu                 sync.RWMutex
	subs               []subscriber[T]
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

func (b *Broker[T]) PublishMustDeliver(event T) {
	if b.closed.Load() {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	timeout := b.mustDeliverTimeout
	for _, s := range b.subs {
		select {
		case s.ch <- event:
			b.delivered.Add(1)
		case <-time.After(timeout):
			b.dropped.Add(1)
		}
	}
}

func (b *Broker[T]) Subscribe(id string, bufSize int) <-chan T {
	if bufSize <= 0 {
		bufSize = defaultBufferSize
	}
	ch := make(chan T, bufSize)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = append(b.subs, subscriber[T]{id: id, ch: ch})
	return ch
}

func (b *Broker[T]) Unsubscribe(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, s := range b.subs {
		if s.id == id {
			close(s.ch)
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			return
		}
	}
}

func (b *Broker[T]) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
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
