package pubsub

import (
	"sync"
	"testing"
	"time"
)

func TestBroker_PublishLossy(t *testing.T) {
	b := NewBroker[int]()
	ch := b.Subscribe("t", 4)
	_ = b.Subscribe("slow", 1)

	for i := 0; i < 4; i++ {
		b.Publish(i)
	}

	for i := 0; i < 4; i++ {
		select {
		case v := <-ch:
			if v != i {
				t.Errorf("expected %d, got %d", i, v)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}

	b.Unsubscribe("t")
	b.Unsubscribe("slow")
}

func TestBroker_PublishMustDeliver(t *testing.T) {
	b := NewBroker[int]()
	ch := b.Subscribe("t", 1)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-done
		select {
		case v := <-ch:
			if v != 99 {
				t.Errorf("expected 99, got %d", v)
			}
		case <-time.After(500 * time.Millisecond):
			t.Error("expected to receive event")
		}
	}()

	b.PublishMustDeliver(99)
	close(done)
	wg.Wait()
	b.Unsubscribe("t")
}

func TestBroker_TryPublishMustDeliverReportsUnavailable(t *testing.T) {
	b := NewBroker[int]()
	if b.TryPublishMustDeliver(1) {
		t.Fatal("empty broker should report false so callers can fall back")
	}

	ch := b.Subscribe("t", 1)
	if !b.TryPublishMustDeliver(2) {
		t.Fatal("open broker with a subscriber should deliver")
	}
	select {
	case got := <-ch:
		if got != 2 {
			t.Errorf("event = %d, want 2", got)
		}
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}

	b.Close()
	if b.TryPublishMustDeliver(3) {
		t.Fatal("closed broker should report false")
	}
}

func TestBroker_PublishMustDeliverTimeout(t *testing.T) {
	b := NewBroker[int]()
	b.mustDeliverTimeout = 10 * time.Millisecond
	ch := b.Subscribe("full", 1)
	// Fill the buffer so the next send blocks.
	b.Publish(1)

	droppedBefore := b.Dropped()
	b.PublishMustDeliver(2)
	if b.Dropped() <= droppedBefore {
		t.Error("expected drop when channel is full")
	}
	b.Unsubscribe("full")
	<-ch
}

func TestBroker_DroppedCount(t *testing.T) {
	b := NewBroker[int]()
	ch := b.Subscribe("t", 2)

	b.Publish(1)
	b.Publish(2)
	b.Publish(3)

	<-ch
	<-ch
	if b.Dropped() < 1 {
		t.Error("expected at least one dropped event")
	}
	b.Unsubscribe("t")
}

func TestBroker_DeliveredCount(t *testing.T) {
	b := NewBroker[int]()
	ch := b.Subscribe("t", 4)

	for i := 0; i < 4; i++ {
		b.Publish(i)
	}

	time.Sleep(10 * time.Millisecond)
	if b.Delivered() != 4 {
		t.Errorf("expected 4 delivered, got %d", b.Delivered())
	}
	for i := 0; i < 4; i++ {
		<-ch
	}
	b.Unsubscribe("t")
}

func TestBroker_Unsubscribe(t *testing.T) {
	b := NewBroker[int]()
	b.Subscribe("a", 4)
	b.Subscribe("b", 4)

	if b.SubscriberCount() != 2 {
		t.Errorf("expected 2 subscribers, got %d", b.SubscriberCount())
	}

	b.Unsubscribe("a")
	if b.SubscriberCount() != 1 {
		t.Errorf("expected 1 subscriber, got %d", b.SubscriberCount())
	}

	b.Publish(1)
	time.Sleep(10 * time.Millisecond)
	if b.Delivered() != 1 {
		t.Errorf("expected 1 delivery to remaining subscriber, got %d", b.Delivered())
	}
	b.Unsubscribe("b")
}

func TestBroker_Close(t *testing.T) {
	b := NewBroker[int]()
	ch := b.Subscribe("t", 4)
	b.Close()

	if b.SubscriberCount() != 0 {
		t.Error("expected 0 subscribers after close")
	}

	_, open := <-ch
	if open {
		t.Error("expected channel closed after Close")
	}

	b.Publish(1)
	b.PublishMustDeliver(2)
	if b.Delivered() != 0 {
		t.Error("expected no deliveries after close")
	}
}

func TestBroker_ZeroSubscribers(t *testing.T) {
	b := NewBroker[int]()
	b.Publish(1)
	b.PublishMustDeliver(2)
	if b.Delivered() != 0 {
		t.Error("expected no deliveries with zero subscribers")
	}
}

func TestBroker_ConcurrentPublish(t *testing.T) {
	b := NewBroker[int]()
	ch := b.Subscribe("t", 2048)

	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				b.Publish(i)
				b.PublishMustDeliver(i)
			}
		}()
	}
	wg.Wait()

	time.Sleep(50 * time.Millisecond)
	total := b.Delivered() + b.Dropped()
	if total != 2000 {
		t.Errorf("expected 2000 total (delivered+dropped), got %d", total)
	}

	b.Unsubscribe("t")
	for range ch {
	}
}

func TestBroker_DefaultBufferSize(t *testing.T) {
	b := NewBroker[int]()
	ch := b.Subscribe("t", 0)

	done := make(chan struct{})
	go func() {
		for i := 0; i < defaultBufferSize; i++ {
			b.Publish(i)
		}
		close(done)
	}()
	<-done

	if b.Dropped() > 0 {
		t.Errorf("expected no drops with default buffer, got %d", b.Dropped())
	}
	b.Unsubscribe("t")

	drained := 0
	for range ch {
		drained++
	}
	if drained < defaultBufferSize {
		t.Errorf("expected at least %d events, drained %d", defaultBufferSize, drained)
	}
}

func TestBroker_MultipleSubscribers(t *testing.T) {
	b := NewBroker[int]()
	ch1 := b.Subscribe("a", 4)
	ch2 := b.Subscribe("b", 4)

	b.Publish(1)

	select {
	case v := <-ch1:
		if v != 1 {
			t.Errorf("subscriber a: expected 1, got %d", v)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber a timed out")
	}
	select {
	case v := <-ch2:
		if v != 1 {
			t.Errorf("subscriber b: expected 1, got %d", v)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber b timed out")
	}

	b.Unsubscribe("a")
	b.Unsubscribe("b")
}

// TestBroker_SubscribeAfterClose (yaah-8jou.2) pins that subscribing to
// a closed broker returns an already-closed channel (so a ranging
// consumer exits) and does not register a live subscription.
func TestBroker_SubscribeAfterClose(t *testing.T) {
	b := NewBroker[int]()
	b.Close()

	ch := b.Subscribe("late", 1)
	if _, open := <-ch; open {
		t.Fatal("expected already-closed channel when subscribing after Close")
	}
	if b.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers after Subscribe on closed broker, got %d", b.SubscriberCount())
	}

	// Publish after the late subscribe must still be a no-op.
	b.Publish(1)
	b.PublishMustDeliver(2)
	if b.Delivered() != 0 {
		t.Errorf("expected no deliveries after close, got %d", b.Delivered())
	}
}

// TestPublishMustDeliver_concurrentCloseDoesNotPanic pins the safety
// invariant that delivery holds the read lock while Close holds the write
// lock, so a send can never race a channel close (send-on-closed-channel
// panic). Exercise it repeatedly with a slow subscriber to widen the
// interleaving window.
func TestPublishMustDeliver_concurrentCloseDoesNotPanic(t *testing.T) {
	for i := 0; i < 50; i++ {
		b := NewBroker[int]()
		b.Subscribe("slow", 1) // nobody reads; fills so MustDeliver waits

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			b.PublishMustDeliver(42)
		}()
		go func() {
			defer wg.Done()
			b.Close()
		}()
		wg.Wait() // a panic in either goroutine fails the test
	}
}

// TestBroker_SubscribeHandle_CancelsExactlyOneSubscription (yaah-8jou.3)
// pins that a cancel handle detaches exactly its own registration, even
// though SubscribeHandle subscriptions share an empty id with each other
// and other subscribers share a non-empty one.
func TestBroker_SubscribeHandle_CancelsExactlyOneSubscription(t *testing.T) {
	b := NewBroker[int]()
	chA, cancelA := b.SubscribeHandle(4)
	chB, _ := b.SubscribeHandle(4)
	chDup1 := b.Subscribe("dup", 4)
	chDup2 := b.Subscribe("dup", 4)

	cancelA()

	if _, open := <-chA; open {
		t.Error("expected cancelled channel to be closed")
	}
	if b.SubscriberCount() != 3 {
		t.Fatalf("expected 3 subscribers after one cancel, got %d", b.SubscriberCount())
	}

	b.Publish(7)
	for i, ch := range []<-chan int{chB, chDup1, chDup2} {
		select {
		case v := <-ch:
			if v != 7 {
				t.Errorf("subscriber %d: expected 7, got %d", i, v)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d did not receive the event", i)
		}
	}
}

// TestBroker_SubscribeHandle_CancelIsIdempotent (yaah-8jou.3): calling the
// handle twice must not double-close the channel.
func TestBroker_SubscribeHandle_CancelIsIdempotent(t *testing.T) {
	b := NewBroker[int]()
	_, cancel := b.SubscribeHandle(4)

	cancel()
	cancel()

	if b.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers after cancel, got %d", b.SubscriberCount())
	}
	b.Publish(1)
}

// TestBroker_SubscribeHandle_CancelAfterClose (yaah-8jou.3): Close nils the
// subscriber list, so a later cancel must be a no-op rather than a panic or
// a double close.
func TestBroker_SubscribeHandle_CancelAfterClose(t *testing.T) {
	b := NewBroker[int]()
	ch, cancel := b.SubscribeHandle(4)

	b.Close()
	cancel()
	cancel()

	if _, open := <-ch; open {
		t.Error("expected channel closed by Close")
	}
	if b.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers, got %d", b.SubscriberCount())
	}
}

// TestBroker_SubscribeHandle_AfterClose mirrors TestBroker_SubscribeAfterClose
// for the handle path: a closed channel and a no-op cancel.
func TestBroker_SubscribeHandle_AfterClose(t *testing.T) {
	b := NewBroker[int]()
	b.Close()

	ch, cancel := b.SubscribeHandle(1)
	if _, open := <-ch; open {
		t.Fatal("expected already-closed channel when subscribing after Close")
	}
	cancel()
	if b.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers, got %d", b.SubscriberCount())
	}
}

// TestBroker_Unsubscribe_RemovesAllDuplicates (yaah-8jou.3): an id may be
// held by several subscribers, and Unsubscribe must detach every match
// rather than returning after the first.
func TestBroker_Unsubscribe_RemovesAllDuplicates(t *testing.T) {
	b := NewBroker[int]()
	dup1 := b.Subscribe("dup", 4)
	dup2 := b.Subscribe("dup", 4)
	keep := b.Subscribe("keep", 4)

	b.Unsubscribe("dup")

	if b.SubscriberCount() != 1 {
		t.Fatalf("expected 1 subscriber after Unsubscribe, got %d", b.SubscriberCount())
	}
	for i, ch := range []<-chan int{dup1, dup2} {
		if _, open := <-ch; open {
			t.Errorf("duplicate %d: expected closed channel", i)
		}
	}

	b.Publish(3)
	select {
	case v := <-keep:
		if v != 3 {
			t.Errorf("expected 3, got %d", v)
		}
	case <-time.After(time.Second):
		t.Fatal("remaining subscriber did not receive the event")
	}
}

// TestBroker_PublishMustDeliver_SharedDeadline (yaah-8jou.4): the MustDeliver
// timeout is one budget for the whole fan-out, so N stalled subscribers cost
// about 1x timeout instead of Nx, and all of them are still counted dropped.
func TestBroker_PublishMustDeliver_SharedDeadline(t *testing.T) {
	const stalled = 5
	timeout := 50 * time.Millisecond

	b := NewBroker[int]()
	b.mustDeliverTimeout = timeout

	for i := 0; i < stalled; i++ {
		b.Subscribe("full", 1)
		b.Publish(0) // fill the buffer so every later send blocks
	}

	droppedBefore := b.Dropped()
	deliveredBefore := b.Delivered()

	start := time.Now()
	b.PublishMustDeliver(1)
	elapsed := time.Since(start)

	if got := b.Dropped() - droppedBefore; got != stalled {
		t.Errorf("dropped = %d, want %d", got, stalled)
	}
	if got := b.Delivered() - deliveredBefore; got != 0 {
		t.Errorf("delivered = %d, want 0", got)
	}
	// Per-subscriber timing would take stalled*timeout; 2.5x timeout leaves a
	// generous margin for a loaded CI runner while still ruling it out.
	if limit := timeout + timeout*3/2; elapsed > limit {
		t.Errorf("fan-out took %v, want at most %v (one shared deadline)", elapsed, limit)
	}
}
