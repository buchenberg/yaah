package memory

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/observability"
)

// embedWarnInterval is the minimum spacing between embedding-failure
// warnings. Embedding is best-effort — vector search degrades to empty
// results while FTS keeps working — so one surfaced failure per
// interval is enough signal without flooding the log while an embed
// server is down or unreachable.
const embedWarnInterval = 30 * time.Second

// embedWarnLimiter rate-limits embedding-failure warnings: at most one
// slog.Warn per interval, with the count of failures suppressed since
// the last emitted warning folded into the next line. The zero value is
// usable (default interval, time.Now clock), matching HTTPEmbedder.
type embedWarnLimiter struct {
	mu         sync.Mutex
	interval   time.Duration
	last       time.Time
	suppressed int
	nowFn      func() time.Time
}

// warn logs an embedding failure unless the quiet window is active. It
// reports whether the warning was emitted. The mutex guards all limiter
// state, including the interval and clock snapshots; only the slog call
// runs outside it.
func (l *embedWarnLimiter) warn(op string, err error) bool {
	l.mu.Lock()
	interval := l.interval
	if interval <= 0 {
		interval = embedWarnInterval
	}
	nowFn := l.nowFn
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	if !l.last.IsZero() && now.Sub(l.last) < interval {
		l.suppressed++
		l.mu.Unlock()
		return false
	}
	suppressed := l.suppressed
	l.suppressed = 0
	l.last = now
	l.mu.Unlock()

	attrs := []any{"op", op, "err", err}
	if suppressed > 0 {
		attrs = append(attrs, "suppressed", suppressed)
	}
	slog.Warn("memory: embedding failed", attrs...)
	return true
}

// recordEmbedFailure surfaces one embedding failure (GitHub #311): the
// OTel yaah.memory.embed_failures counter increments unconditionally,
// while the warning log is rate-limited. op names the failing operation
// ("embed_memory", "embed_message", "search_memory_vector",
// "search_messages_vector", "reconcile_embeddings").
func (d *DB) recordEmbedFailure(op string, err error) {
	observability.RecordMemoryEmbedFailure(context.Background(), op)
	d.embedWarn.warn(op, err)
}
