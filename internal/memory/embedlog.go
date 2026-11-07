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
	ok, suppressed := l.allow()
	if !ok {
		return false
	}
	attrs := []any{"op", op, "err", err}
	if suppressed > 0 {
		attrs = append(attrs, "suppressed", suppressed)
	}
	slog.Warn("memory: embedding failed", attrs...)
	return true
}

// allow reports whether a warning may be emitted now, along with the count
// of warnings suppressed since the last emitted one. Callers that need their
// own message (not warn's fixed "embedding failed") gate on this and log
// themselves. The mutex guards all limiter state; the caller logs outside it.
func (l *embedWarnLimiter) allow() (bool, int) {
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
		return false, 0
	}
	suppressed := l.suppressed
	l.suppressed = 0
	l.last = now
	l.mu.Unlock()
	return true, suppressed
}

// dimWarnLimiter rate-limits the stale-dimensionality warning per stored width
// (yaah-shq). It is deliberately not one shared window: a store can hold rows
// from several past embedding models (384 and 512 and 768), and a single
// limiter let the first width's warning suppress the others for a whole
// interval, so the doc's "one warning per stale width" was not what the code
// did. Each width gets its own embedWarnLimiter, so every width warns once per
// interval and repeats stay rate-limited. The zero value is usable and safe for
// concurrent use.
type dimWarnLimiter struct {
	mu      sync.Mutex
	byWidth map[int]*embedWarnLimiter
}

// allow reports whether the warning for one width may be emitted now, along
// with the number of that width's warnings suppressed since the last emitted
// one.
func (l *dimWarnLimiter) allow(width int) (bool, int) {
	return l.limiter(width).allow()
}

// limiter returns the per-width limiter, creating it on first use. The returned
// limiter carries its own mutex, so this map's lock is held only long enough to
// fetch it and a caller's log call never runs under it.
func (l *dimWarnLimiter) limiter(width int) *embedWarnLimiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byWidth == nil {
		l.byWidth = make(map[int]*embedWarnLimiter)
	}
	lim, ok := l.byWidth[width]
	if !ok {
		lim = &embedWarnLimiter{}
		l.byWidth[width] = lim
	}
	return lim
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

// warnEmbedDimMismatch surfaces a store left at a previous embedding model's
// dimensionality (yaah-shq): those rows cannot be compared with the current
// model's query vector, so the vector search scores them by keyword fallback.
// The state repeats on every search until the rows are re-embedded, so it is
// rate-limited exactly like an embed failure — one warning per interval with
// the suppressed count folded into the next line — instead of vanishing
// silently. The window is per stored width, so a store left at several widths
// names every one of them.
func (d *DB) warnEmbedDimMismatch(storedDim, queryDim, rows int) bool {
	ok, suppressed := d.dimWarn.allow(storedDim)
	if !ok {
		return false
	}
	attrs := []any{"stored_dim", storedDim, "query_dim", queryDim, "rows", rows}
	if suppressed > 0 {
		attrs = append(attrs, "suppressed", suppressed)
	}
	slog.Warn("memory: stored embedding dimensions differ from the embedder; those rows use keyword fallback", attrs...)
	return true
}
