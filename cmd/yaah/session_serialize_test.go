package yaah

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/control"
)

// The run gate must queue a second run until the first returns: overlapping
// runs snapshot s.messages/msgIdx from the same pre-turn state.
func TestAgentSession_SerializesRuns(t *testing.T) {
	s := &agentSession{}

	release := make(chan struct{})
	closed := false
	t.Cleanup(func() {
		if !closed {
			close(release)
		}
	})

	var mu sync.Mutex
	var order []string
	firstHeld := make(chan struct{})
	go s.serialize(func() {
		mu.Lock()
		order = append(order, "first")
		mu.Unlock()
		close(firstHeld)
		<-release
	})
	<-firstHeld

	secondDone := make(chan struct{})
	go s.serialize(func() {
		mu.Lock()
		order = append(order, "second")
		mu.Unlock()
		close(secondDone)
	})

	select {
	case <-secondDone:
		t.Fatal("second run entered while the first held the run gate")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	closed = true
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second run never started after the first released the gate")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("run order = %v; want [first second]", order)
	}
}

// :compact replaces s.messages/s.msgIdx, so it must not start while a run
// holds the gate. It refuses immediately (frontends must not block) instead
// of queuing behind the run.
func TestAgentSession_CompactRefusesDuringRun(t *testing.T) {
	ch := make(chan control.Msg, 4)
	s := &agentSession{ctrlCh: ch}

	release := make(chan struct{})
	closed := false
	t.Cleanup(func() {
		if !closed {
			close(release)
		}
	})

	held := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		s.serialize(func() {
			close(held)
			<-release
		})
	}()
	<-held

	ran := make(chan bool, 1)
	go func() { ran <- s.Compact() }()

	select {
	case ok := <-ran:
		if ok {
			t.Fatal("Compact reported success while a run held the gate")
		}
	case <-time.After(time.Second):
		t.Fatal("Compact blocked behind an in-flight run")
	}

	select {
	case msg := <-ch:
		status, ok := msg.(*control.Status)
		if !ok || !strings.Contains(status.Text, "cannot compact") {
			t.Fatalf("refusal status = %#v; want the cannot-compact notice", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("Compact gave no refusal status")
	}

	close(release)
	closed = true
	<-finished

	if !s.Compact() {
		t.Fatal("Compact refused after the gate released")
	}
	select {
	case msg := <-ch:
		status, ok := msg.(*control.Status)
		if !ok || !strings.Contains(status.Text, "small enough") {
			t.Fatalf("compact status = %#v; want the empty-transcript notice", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("compact produced no status after the gate released")
	}
}
