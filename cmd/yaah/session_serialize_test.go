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

// :compact replaces s.messages/s.msgIdx, so it must share the run gate with
// prompt runs rather than branch the transcript mid-turn.
func TestAgentSession_CompactWaitsForRun(t *testing.T) {
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
	go s.serialize(func() {
		close(held)
		<-release
	})
	<-held

	compactDone := make(chan struct{})
	go func() {
		s.Compact()
		close(compactDone)
	}()

	select {
	case msg := <-ch:
		t.Fatalf("compact ran while a run held the gate: %T", msg)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	closed = true
	select {
	case <-compactDone:
	case <-time.After(time.Second):
		t.Fatal("compact never completed after the gate released")
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
