package supervision

import (
	"errors"
	"testing"
)

func TestRegistry_InjectDeliversToRegisteredRun(t *testing.T) {
	r := NewRegistry()
	ch := make(chan string, SteerBuffer)
	r.Register("scope:sub-1", ch)

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
	r.Register("scope:sub-2", ch)
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
	r.Register("scope:wedged", ch)
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
	r.Register("scope:closing", ch)
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
	r.Register("scope:nowhere", nil)
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
	r.Register("scope:c", make(chan string, SteerBuffer))
	r.Register("scope:a", make(chan string, SteerBuffer))
	r.Register("scope:b", make(chan string, SteerBuffer))
	got := r.ScopeIDs()
	want := []string{"scope:a", "scope:b", "scope:c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ScopeIDs = %v, want %v", got, want)
		}
	}
}
