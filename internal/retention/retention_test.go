package retention

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/memory"
)

// throughMax is the ordinal cap the evictor itself probes with: no
// retained path exceeds it.
const throughMax = int(^uint(0) >> 1)

// newFixture seeds a state database and a trace store with one main-agent
// owner path (the session id) and one sub-agent path, wired the way the
// runner records them, and returns the pieces a delete flow touches.
type fixture struct {
	t         *testing.T
	db        *memory.DB
	store     *shepherdtrace.SQLiteTraceStore
	tracePath string
	evictor   *Evictor
	subOwner  string
	mainFacts []string
	subFacts  []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace.db")

	db, err := memory.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("NewSQLiteTraceStore: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	f := &fixture{t: t, db: db, store: store, tracePath: tracePath, evictor: NewEvictor(tracePath)}
	f.mainFacts = f.appendFacts("intent:main", "sess-main", "sess-main")
	f.subOwner = "sub-researcher-sess-main-1730000000"
	f.subFacts = f.appendFacts("intent:sub", f.subOwner, "sess-main")
	if err := db.CreateSession(memory.Session{ID: "sess-main", StartedAt: 1730000000, CWD: dir}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := db.RecordTraceOwner("sess-main", "sess-main", "main"); err != nil {
		t.Fatalf("RecordTraceOwner main: %v", err)
	}
	if err := db.RecordTraceOwner(f.subOwner, "sess-main", "subagent"); err != nil {
		t.Fatalf("RecordTraceOwner sub: %v", err)
	}
	return f
}

func (f *fixture) appendFacts(intent, owner, parentSession string) []string {
	f.t.Helper()
	receipt, err := f.store.Append(context.Background(), shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: intent,
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID: owner,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Capture,
				SchemaRef: "yaah.test.turn.v1",
				Payload:   map[string]any{"session": parentSession},
			}},
		}},
	})
	if err != nil {
		f.t.Fatalf("Append(%s): %v", intent, err)
	}
	return receipt.FactIDs
}

func (f *fixture) wantEvicted(owner string) {
	f.t.Helper()
	if _, err := f.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, owner, throughMax, shepherdtrace.ModeBoth); !errors.Is(err, shepherdtrace.ErrOwnerEvicted) {
		f.t.Fatalf("path %s after delete: want ErrOwnerEvicted, got %v", owner, err)
	}
}

// math32Max is the through value the evictor itself uses: an ordinal cap
// no retained path exceeds.
const math32Max = int(^uint(0) >> 1)

func TestDeleterEvictsKernelFirstThenCascades(t *testing.T) {
	f := newFixture(t)

	if err := f.evictor.EvictSession(f.db, "sess-main"); err != nil {
		t.Fatalf("EvictSession: %v", err)
	}
	f.wantEvicted("sess-main")
	f.wantEvicted(f.subOwner)

	// The state model is untouched until the cascade runs: the session and
	// its mapping still resolve (a crash here is retry-able, the safe
	// direction).
	if _, err := f.db.GetSession("sess-main"); err != nil {
		t.Fatalf("state row touched before the cascade: %v", err)
	}
	if parent, err := f.db.TraceOwnerParent(f.subOwner); err != nil || parent != "sess-main" {
		t.Fatalf("trace_owners touched before the cascade: %q %v", parent, err)
	}

	if err := f.db.DeleteSession("sess-main"); err != nil {
		t.Fatalf("state cascade: %v", err)
	}
	// The raw memory.DB passes the driver's sql.ErrNoRows through (the
	// sessions/dbStore adapter is what normalizes to ErrSessionNotFound).
	if _, err := f.db.GetSession("sess-main"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session survived the cascade: %v", err)
	}
	if parent, err := f.db.TraceOwnerParent(f.subOwner); err == nil {
		t.Errorf("trace_owners mapping survived the cascade: %q", parent)
	}

	// The sweep reclaims the evicted records and their uncited witnesses;
	// the root witness is the only unconditional survivor.
	if err := f.evictor.SweepNow(); err != nil {
		t.Fatalf("SweepNow: %v", err)
	}
	count, err := f.store.FactCount(context.Background())
	if err != nil {
		t.Fatalf("FactCount: %v", err)
	}
	if count != 1 {
		t.Errorf("records after sweep = %d, want 1 (the root witness)", count)
	}
}

func TestDeleterIsRetryableAcrossCrashWindows(t *testing.T) {
	f := newFixture(t)

	// Crash window 1: before eviction — plain retry.
	if err := f.evictor.EvictSession(f.db, "sess-main"); err != nil {
		t.Fatalf("EvictSession: %v", err)
	}
	// Crash window 2: eviction committed, cascade never ran. A register
	// maps the same deleted id again: the probes must skip, not fail.
	if err := f.evictor.EvictSession(f.db, "sess-main"); err != nil {
		t.Fatalf("retried EvictSession after eviction already committed: %v", err)
	}
	if err := f.db.DeleteSession("sess-main"); err != nil {
		t.Fatalf("cascade after retried eviction: %v", err)
	}
}

func TestMissingTraceStoreEvictsNothingAndStillDeletes(t *testing.T) {
	dir := t.TempDir()
	db, err := memory.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// No trace.db was ever created — a configuration with the trace store
	// elsewhere, or a session that never ran an agent.
	if err := NewEvictor(filepath.Join(dir, "absent", "trace.db")).EvictSession(db, "sess-none"); err != nil {
		t.Fatalf("EvictSession with missing trace store: %v", err)
	}
}
