package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestToolConcurrency_Name(t *testing.T) {
	m := NewToolConcurrencyMiddleware(4)
	if m.Name() != "tool_concurrency" {
		t.Errorf("Name() = %q, want %q", m.Name(), "tool_concurrency")
	}
}

func TestToolConcurrency_NoOpWhenUnlimited(t *testing.T) {
	m := NewToolConcurrencyMiddleware(0)
	if err := m.Acquire(context.Background()); err != nil {
		t.Errorf("Acquire() with max=0 should be a no-op, got %v", err)
	}
	// Release must also be safe when unlimited.
	m.Release()
}

// --- Acquire gates concurrency ---

func TestToolConcurrency_AcquireLimitsConcurrency(t *testing.T) {
	const cap = 2
	m := NewToolConcurrencyMiddleware(cap)

	// Take all slots.
	if err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("second Acquire: %v", err)
	}

	// Third acquire must block. Verify with a deadline that elapses
	// while the acquire is blocked.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := m.Acquire(ctx); err == nil {
		t.Fatal("third Acquire should have failed (cap reached), got nil")
	}

	// Release one slot; acquire must now succeed.
	m.Release()
	if err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
}

func TestToolConcurrency_AcquireHonoursContext(t *testing.T) {
	const cap = 1
	m := NewToolConcurrencyMiddleware(cap)

	// Saturate.
	if err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Cancelled context → Acquire returns immediately with the err.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Acquire(ctx); !contextIsCancelled(err) {
		t.Fatalf("Acquire with cancelled ctx: got %v, want context.Canceled", err)
	}
}

func TestToolConcurrency_ReleaseSafeOnUnlimited(t *testing.T) {
	// Must not panic when there was no matching Acquire.
	m := NewToolConcurrencyMiddleware(0)
	m.Release()
	m.Release()
}

func TestToolConcurrency_ReleaseIsIdempotent(t *testing.T) {
	const cap = 1
	m := NewToolConcurrencyMiddleware(cap)
	if err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	m.Release()
	// Second release must not pop more than was acquired.
	m.Release()
}

// --- concurrent stress: cap holds ---

func TestToolConcurrency_HoldsCapUnderLoad(t *testing.T) {
	const cap = 4
	const callers = 40
	m := NewToolConcurrencyMiddleware(cap)

	var inFlight atomic.Int32
	var maxSeen atomic.Int32
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			if err := m.Acquire(context.Background()); err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			cur := inFlight.Add(1)
			for {
				old := maxSeen.Load()
				if cur <= old || maxSeen.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			inFlight.Add(-1)
			m.Release()
		}()
	}
	wg.Wait()

	if got := maxSeen.Load(); got > cap {
		t.Errorf("observed %d concurrent holders, cap was %d", got, cap)
	}
}

// contextIsCancelled returns true if err is a context cancellation.
func contextIsCancelled(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}
