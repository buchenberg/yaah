// Package retention implements the caller's half of the owner-scoped
// eviction design for session deletion: the kernel-first ordering
// (docs/retention-gc.md §5) and the coalesced garbage-collection sweep
// (§6, decision 6). The kernel side — EvictOwnerPath / CollectGarbage and
// their laws — lives in goatherd-kernel (v0.20.0); this package only
// sequences it against the state model.
package retention

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"sync"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/memory"
)

// Evictor is the kernel-first half of session deletion, wired on a
// sessions.Registry via WithEvictor at any entry point that knows the
// trace store path. A registry without one keeps Delete's plain state
// cascade — the pre-retention behavior.
type Evictor struct {
	tracePath string
}

// NewEvictor builds the evictor for the trace store at tracePath
// (<yaah home>/trace.db or the shepherd_trace_path override).
func NewEvictor(tracePath string) *Evictor {
	return &Evictor{tracePath: tracePath}
}

// EvictSession evicts every trace owner path carrying the session's facts,
// before any state row is touched:
//
//  1. the owner ids are collected first — the trace_owners mapping is the
//     only record of the sub-* synthetic owner ids, and the cascade deletes
//     it last, so reading first is what makes a crashed delete retry-able;
//  2. each path is evicted with the frontier ack, because the session's own
//     cut rows die with the session (design §5);
//
// A nil db (a registry over a view fake) still evicts the session's own
// path: the mapping only adds the sub-owner ids. Tolerances, both
// deliberate: a missing trace database means this process never traced
// anything and evicts nothing (nil); an already-evicted path (a delete
// that crashed after eviction, before the cascade — the safe direction)
// is skipped, so the retry completes instead of failing forever.
func (e *Evictor) EvictSession(db *memory.DB, sessionID string) error {
	if _, err := os.Stat(e.tracePath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat trace store: %w", err)
	}

	var owners []string
	if db != nil {
		var err error
		owners, err = db.TraceOwnersForSession(sessionID)
		if err != nil {
			return fmt.Errorf("read trace owners: %w", err)
		}
	}
	// The main agent's trace owner is the session id itself; the mapping
	// covers the sub-agents. Evict both, deduplicated.
	candidates := make([]string, 0, 1+len(owners))
	candidates = append(candidates, sessionID)
	for _, owner := range owners {
		if owner != sessionID {
			candidates = append(candidates, owner)
		}
	}

	ctx := context.Background()
	store, err := shepherdtrace.NewSQLiteTraceStore(e.tracePath)
	if err != nil {
		return fmt.Errorf("open trace store: %w", err)
	}
	defer store.Close(ctx)

	for _, owner := range candidates {
		// Evict only paths with retained content: an unknown path must not
		// gain a tombstone because of a spelling mismatch, and an empty
		// path has nothing of the session's to make unreachable.
		prefix, err := store.ReadOwnerPrefix(ctx, shepherdtrace.TrustedReadContext, owner, math.MaxInt, shepherdtrace.ModeBoth)
		if err != nil {
			if errors.Is(err, shepherdtrace.ErrOwnerEvicted) {
				continue // a prior delete attempt already evicted this path
			}
			return fmt.Errorf("probe trace path: %w", err)
		}
		if prefix.FactIDs() == nil {
			continue
		}

		if _, err := store.EvictOwnerPath(ctx, shepherdtrace.TrustedAppendContext, owner, true); err != nil {
			if errors.Is(err, shepherdtrace.ErrOwnerEvicted) {
				continue // raced a concurrent delete of the same session
			}
			return fmt.Errorf("evict trace owner %s: %w", owner, err)
		}
	}
	return nil
}

// AfterDelete schedules the garbage-collection sweep for this trace store.
// It runs after the state cascade committed, so a crash before it only
// leaves already-unreachable records behind — the next sweep claims them.
func (e *Evictor) AfterDelete() {
	scheduleSweep(e.tracePath)
}

// SweepNow runs one CollectGarbage synchronously. Scheduling coalesces
// bursts of deletes into one of these; it is exported so tests (and a
// future retention-policy janitor) can sweep on demand.
func (e *Evictor) SweepNow() error {
	if _, err := os.Stat(e.tracePath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	store, err := shepherdtrace.NewSQLiteTraceStore(e.tracePath)
	if err != nil {
		return fmt.Errorf("open trace store: %w", err)
	}
	defer store.Close(context.Background())

	receipt, err := store.CollectGarbage(context.Background(), shepherdtrace.TrustedAppendContext)
	if err != nil {
		return err
	}
	if receipt.DeletedRecords > 0 || receipt.DeletedContexts > 0 || receipt.DeletedAppendIntents > 0 {
		slog.Info("retention sweep reclaimed trace records",
			"records", receipt.DeletedRecords,
			"contexts", receipt.DeletedContexts,
			"intents", receipt.DeletedAppendIntents,
			"witnesses", len(receipt.ReclaimedWitnessIDs))
	}
	return nil
}

// sweepDelay is the delete-burst coalescing window: deletes landing inside
// it share one sweep of the (global) store.
const sweepDelay = 5 * time.Second

// sweepTimers holds one pending debounce timer per trace store, guarded by
// sweepMu: while a sweep is pending more deletes are absorbed, and a sweep
// is never scheduled twice concurrently.
var (
	sweepMu     sync.Mutex
	sweepTimers = map[string]*time.Timer{}
)

// scheduleSweep coalesces deletes into one sweep per trace store: while a
// sweep for a path is pending, more deletes are absorbed; a request during
// a running sweep flags a re-run instead of starting a second one. The
// sweep is global (§6), so per-delete sweeps would waste the write lock.
// Best-effort by design: eviction already made the content unreachable, so
// a sweep that never runs (a CLI exiting first) only defers reclamation.
func scheduleSweep(tracePath string) {
	sweepMu.Lock()
	defer sweepMu.Unlock()
	if _, pending := sweepTimers[tracePath]; pending {
		return
	}
	sweepTimers[tracePath] = time.AfterFunc(sweepDelay, func() {
		sweepMu.Lock()
		delete(sweepTimers, tracePath)
		sweepMu.Unlock()

		e := &Evictor{tracePath: tracePath}
		if err := e.SweepNow(); err != nil {
			slog.Warn("retention sweep failed; records stay unreachable until the next sweep", "trace", tracePath, "err", err)
		}
	})
}
