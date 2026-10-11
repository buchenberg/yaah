package supervision

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistry_InjectDeliversToRegisteredRun(t *testing.T) {
	r := NewRegistry()
	ch := make(chan string, SteerBuffer)
	r.Register("scope:sub-1", Run{SteerCh: ch})

	if err := r.Inject("scope:sub-1", "try a different approach"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	select {
	case got := <-ch:
		if got != "try a different approach" {
			t.Errorf("delivered = %q, want the guidance verbatim", got)
		}
	default:
		t.Fatal("Inject reported success but nothing arrived on the channel")
	}
	if !r.Has("scope:sub-1") {
		t.Error("Has must report the registered run")
	}
}

func TestRegistry_InjectWithoutRun(t *testing.T) {
	r := NewRegistry()
	if err := r.Inject("scope:gone", "x"); !errors.Is(err, ErrNoRun) {
		t.Errorf("Inject at an unregistered scope = %v, want ErrNoRun", err)
	}
}

func TestRegistry_UnregisterThenInject(t *testing.T) {
	r := NewRegistry()
	ch := make(chan string, SteerBuffer)
	r.Register("scope:sub-2", Run{SteerCh: ch})
	r.Unregister("scope:sub-2")
	if err := r.Inject("scope:sub-2", "x"); !errors.Is(err, ErrNoRun) {
		t.Errorf("Inject after Unregister = %v, want ErrNoRun", err)
	}
	r.Unregister("scope:sub-2") // idempotent; a deferred unregister races session close
	if r.Has("scope:sub-2") {
		t.Error("Has after double Unregister must be false")
	}
}

func TestRegistry_InjectFullBufferIsBusyNotBlock(t *testing.T) {
	r := NewRegistry()
	// A full buffer must report busy, not block the supervisor: the
	// run is not consuming guidance (wedged between turns).
	ch := make(chan string, 1)
	r.Register("scope:wedged", Run{SteerCh: ch})
	if err := r.Inject("scope:wedged", "first"); err != nil {
		t.Fatalf("first Inject: %v", err)
	}
	if err := r.Inject("scope:wedged", "second"); !errors.Is(err, ErrSteerBusy) {
		t.Errorf("second Inject = %v, want ErrSteerBusy", err)
	}
}

func TestRegistry_UnregisterBeforeClosePreventsSendOnClosed(t *testing.T) {
	// The lifecycle contract: a run (or session close) unregisters BEFORE
	// its channel closes, so a registry send can never land on a closed
	// channel. This test pins the ordering's safety net — the send side
	// takes the registry lock, so an Unregister that has returned means
	// no concurrent send is in flight.
	r := NewRegistry()
	ch := make(chan string, SteerBuffer)
	r.Register("scope:closing", Run{SteerCh: ch})
	r.Unregister("scope:closing")
	close(ch) // would panic if a send raced it; the unregister guarantees none
	if err := r.Inject("scope:closing", "x"); !errors.Is(err, ErrNoRun) {
		t.Errorf("Inject after close-adjacent unregister = %v, want ErrNoRun", err)
	}
}

// TestRegistry_RegisterNilIsNoOp pins the honest-bridge contract: a nil
// channel registers nothing — a fabricated undrained channel would let
// Inject claim delivery into a void while status listed the scope as
// bridged.
func TestRegistry_RegisterNilIsNoOp(t *testing.T) {
	r := NewRegistry()
	r.Register("scope:nowhere", Run{})
	if r.Has("scope:nowhere") {
		t.Error("a nil channel must not register")
	}
	if err := r.Inject("scope:nowhere", "x"); !errors.Is(err, ErrNoRun) {
		t.Errorf("Inject at a nil-registered scope = %v, want ErrNoRun", err)
	}
	if ids := r.ScopeIDs(); len(ids) != 0 {
		t.Errorf("status must not list a nil registration, got %v", ids)
	}
}

func TestRegistry_ScopeIDsSorted(t *testing.T) {
	r := NewRegistry()
	r.Register("scope:c", Run{SteerCh: make(chan string, SteerBuffer)})
	r.Register("scope:a", Run{SteerCh: make(chan string, SteerBuffer)})
	r.Register("scope:b", Run{SteerCh: make(chan string, SteerBuffer)})
	got := r.ScopeIDs()
	want := []string{"scope:a", "scope:b", "scope:c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ScopeIDs = %v, want %v", got, want)
		}
	}
}

// TestRegistry_CancelInvokesTheRunsCancellation pins the A3 halt bridge:
// Cancel calls the cancellation the run registered, is idempotent by
// context semantics, and reports honestly for scopes without a live,
// haltable run.
func TestRegistry_CancelInvokesTheRunsCancellation(t *testing.T) {
	r := NewRegistry()
	ch := make(chan string, SteerBuffer)

	cancelled := make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-ctx.Done()
		cancelled <- struct{}{}
	}()
	quiesceSrc := make(chan struct{})
	r.Register("scope:run-1", Run{SteerCh: ch, Cancel: cancel, Done: quiesceSrc})

	quiesce, err := r.Cancel("scope:run-1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("the registered cancellation was never invoked")
	}
	// The quiesce channel: closed by the run when it stops — the
	// caller's wait-before-teardown handle.
	if quiesce == nil {
		t.Fatal("Cancel must return the run's quiesce channel")
	}
	select {
	case <-quiesce:
		t.Error("the quiesce channel must not be closed while the run is live")
	default:
	}

	// Idempotent: a second cancel on the same run is the same call on
	// the same context — no error, no second signal needed.
	if _, err := r.Cancel("scope:run-1"); err != nil {
		t.Errorf("second Cancel = %v, want nil (context cancellation is idempotent)", err)
	}

	// No run: honest refusal, never a phantom halt.
	if _, err := r.Cancel("scope:gone"); !errors.Is(err, ErrNoRun) {
		t.Errorf("Cancel at an unregistered scope = %v, want ErrNoRun", err)
	}

	// A run registered without a cancellation is injectable but not
	// haltable — reported, not silently treated as halted.
	r.Register("scope:inject-only", Run{SteerCh: ch})
	if _, err := r.Cancel("scope:inject-only"); !errors.Is(err, ErrNotHaltable) {
		t.Errorf("Cancel at an inject-only run = %v, want ErrNotHaltable", err)
	}

	// After unregister, the run is finished: Cancel reports ErrNoRun.
	// The run stops and closes its quiesce channel, then unregisters:
	// the caller that waited on quiesce knows teardown is safe, and a
	// later Cancel reports ErrNoRun — never a phantom halt.
	close(quiesceSrc)
	r.Unregister("scope:run-1")
	if _, err := r.Cancel("scope:run-1"); !errors.Is(err, ErrNoRun) {
		t.Errorf("Cancel after Unregister = %v, want ErrNoRun", err)
	}
}
