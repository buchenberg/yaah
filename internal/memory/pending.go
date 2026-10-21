package memory

import (
	"log/slog"
	"sync"
	"time"
)

// embedDrainTimeout bounds how long Close waits for in-flight background
// embedding writes. An embedding server that is slow or unreachable must
// never hold up process exit; a dropped embedding is recoverable (the
// reconcile pass re-embeds rows whose embedding IS NULL), so a timeout
// costs at most one deferred embed.
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
	wg     sync.WaitGroup
}

// start registers one in-flight embed. It reports false once drain has
// begun, so a late caller skips the work instead of racing pool shutdown.
func (p *pendingEmbeds) start() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.wg.Add(1)
	return true
}

// done marks one registered embed finished. It must be called exactly once
// for every start that returned true.
func (p *pendingEmbeds) done() {
	p.wg.Done()
}

// drain closes the gate and waits up to timeout for in-flight embeds.
// It returns true when everything finished, false when the wait timed out.
func (p *pendingEmbeds) drain(timeout time.Duration) bool {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()

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
