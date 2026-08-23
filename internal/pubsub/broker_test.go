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

// TestPublishMustDeliver_doesNotBlockSubscribe verifies a slow
// subscriber's wait happens outside the broker lock, so Subscribe and
// Unsubscribe complete while delivery is stalled (finding D2).
func TestPublishMustDeliver_doesNotBlockSubscribe(t *testing.T) {
	b := NewBroker[int]()
	slow := b.Subscribe("slow", 1)
	b.Publish(7) // fills the cap-1 buffer; nobody reads it
	_ = slow

	done := make(chan struct{})
	go func() {
		start := time.Now()
		b.PublishMustDeliver(42) // blocks ~timeout on slow subscriber
		elapsed := time.Now().Sub(start)
		if elapsed < 20*time.Millisecond {
			t.Errorf("expected PublishMustDeliver to wait for slow subscriber, returned in %v", elapsed)
		}
		close(done)
	}()

	subscribeDone := make(chan struct{})
	go func() {
		time.Sleep(5 * time.Millisecond) // let MustDeliver start waiting
		b.Subscribe("fast", 16)
		b.Unsubscribe("fast")
		close(subscribeDone)
	}()

	select {
	case <-subscribeDone:
	case <-time.After(time.Second):
		t.Fatal("Subscribe/Unsubscribe blocked behind slow MustDeliver (head-of-line blocking)")
	}
	<-done
}
