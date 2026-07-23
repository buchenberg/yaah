package memory

import (
	"context"
	"sync"
	"time"
)

const defaultWriteDebounce = 33 * time.Millisecond

type DebouncedWriter struct {
	db       *DB
	mu       sync.Mutex
	pending  map[string]Message
	timer    *time.Timer
	interval time.Duration
}

func NewDebouncedWriter(db *DB) *DebouncedWriter {
	return &DebouncedWriter{
		db:       db,
		pending:  make(map[string]Message),
		interval: defaultWriteDebounce,
	}
}

func (w *DebouncedWriter) Update(ctx context.Context, m Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if shouldFlushNow(m) {
		w.flushPendingLocked(ctx)
		return w.flushOneLocked(ctx, m)
	}

	w.pending[m.ID] = m
	if w.timer == nil {
		w.timer = time.AfterFunc(w.interval, func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.flushPendingLocked(context.Background())
		})
	}
	return nil
}

func (w *DebouncedWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushPendingLocked(context.Background())
}

func shouldFlushNow(m Message) bool {
	return m.Role == "user"
}

func (w *DebouncedWriter) flushPendingLocked(ctx context.Context) error {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	for _, m := range w.pending {
		if err := w.flushOneLocked(ctx, m); err != nil {
			return err
		}
	}
	w.pending = make(map[string]Message)
	return nil
}

func (w *DebouncedWriter) flushOneLocked(ctx context.Context, m Message) error {
	return w.db.AddMessage(m)
}
