package memory

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// embedDrainTimeout bounds how long Close waits for in-flight background
// embedding writes. Before the wait, drain cancels their contexts — an
// embedding server with a long HTTP timeout (30s) would otherwise still
// be mid-call when the pool goes away, which is exactly the race this
// gate exists to prevent. A cancelled or skipped embed is recoverable
// (the reconcile pass re-embeds rows whose embedding IS NULL), so the
// bound costs at most one deferred embed.
const embedDrainTimeout = 5 * time.Second

// pendingEmbeds tracks fire-and-forget embedding goroutines so Close can
// drain them before the SQLite pool goes away.
//
// Both EmbedMemoryAsync and embedMessageAsync hand work to a background
// goroutine that ends in an UPDATE against d.sql. Nothing used to serialize
// that UPDATE against Close, so a short-lived process (yaah acp-serve after
// one prompt, --help paths, REPL /exit) closed the pool while an embed was
// still in flight: the row kept a NULL embedding and the only trace was a
// "store embedding failed: sql: database is closed" warning at shutdown.
//
// The zero value is ready to use.
type pendingEmbeds struct {
	mu     sync.Mutex
	closed bool
	// raise collects the context cancels of every registered embed;
	// drain invokes them so a slow HTTP embed aborts promptly instead
	// of blowing the drain budget.
	raise []context.CancelFunc
	wg    sync.WaitGroup
}

// begin registers one in-flight embed under cancel. ok is false once
// drain has begun — the callee then skips the write instead of racing
// pool shutdown. On ok, the returned finish must be called exactly once
// when the goroutine unwinds; on !ok it is a no-op the caller may still
// call unconditionally.
func (p *pendingEmbeds) begin(cancel context.CancelFunc) (finish func(), ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return func() {}, false
	}
	p.wg.Add(1)
	p.raise = append(p.raise, cancel)
	return p.wg.Done, true
}

// drain closes the gate, cancels the registered embeds' contexts, and
// waits up to timeout for them to unwind. It returns true when every
// registered embed finished, false when the wait timed out.
func (p *pendingEmbeds) drain(timeout time.Duration) bool {
	p.mu.Lock()
	p.closed = true
	raise := p.raise
	p.mu.Unlock()
	for _, cancel := range raise {
		cancel()
	}

	finished := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		return true
	case <-time.After(timeout):
		slog.Warn("memory: embedding writes still in flight at close", "timeout", timeout.String())
		return false
	}
}
