package control

import (
	"context"
	"testing"
	"time"
)

// A cancelled run must deny. Nothing is buffered here, so the branch is chosen
// deterministically: the wait ends on the run's context, not on the timeout.
func TestAwaitAnswerCancelledRunDenies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, ok := awaitAnswer(ctx, make(chan bool), nil, time.Minute)
	if ok || got {
		t.Fatalf("awaitAnswer = (%v, %v), want the zero value and false", got, ok)
	}
}

// The surface-gone and timeout branches keep honoring a decision that landed at
// the same instant: the user answered, and discarding that answer would deny a
// call nobody declined. Both halves of this select return true, so the outcome
// is deterministic even though the select is not.
func TestAwaitAnswerHonorsDecisionWhenSurfaceGoes(t *testing.T) {
	ch := make(chan bool, 1)
	ch <- true
	gone := make(chan struct{})
	close(gone)

	got, ok := awaitAnswer(context.Background(), ch, gone, time.Minute)
	if !ok || !got {
		t.Fatalf("awaitAnswer = (%v, %v), want the buffered decision honored", got, ok)
	}
}

// A dead surface with no decision must return promptly rather than wait out the
// whole bound — the property that keeps a torn-down SSE stream from parking a
// tool call.
func TestAwaitAnswerGoneWithoutDecisionIsFast(t *testing.T) {
	gone := make(chan struct{})
	close(gone)

	begin := time.Now()
	_, ok := awaitAnswer(context.Background(), make(chan bool), gone, time.Minute)
	if ok {
		t.Fatal("awaitAnswer reported a decision from a dead surface with no answer")
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("awaitAnswer waited %v on a closed surface", elapsed)
	}
}
