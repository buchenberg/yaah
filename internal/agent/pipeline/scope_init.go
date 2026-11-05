package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	sandbox "github.com/buchenberg/goatherd-runtime/sandbox"
)

// defaultBusBuffer is the per-subscriber effect bus buffer size used when
// the caller passes zero or a negative value.
const defaultBusBuffer = 64

// TraceSink is the write seam onto the session's Shepherd trace store (plan
// 07, H4). The trace middleware, the runner's per-dispatch usage facts and
// the session teardown depend on this interface instead of the kernel's
// concrete store, so the kernel type appears in one place — the adapter
// below — and a caller (a test, a bench) can substitute its own sink.
//
// The three methods are exactly what those consumers use: append a fact
// batch, publish a frontier cut, and close the handle. Adding a method here
// means the kernel store is the only implementation that needs it.
type TraceSink interface {
	Append(ctx context.Context, auth shepherdtrace.AppendContext, batch shepherdtrace.AppendBatch) (shepherdtrace.AppendReceipt, error)
	PublishFrontier(ctx context.Context, auth shepherdtrace.AppendContext, spec shepherdtrace.FrontierSpec) (shepherdtrace.Frontier, error)
	Close(ctx context.Context) error
}

// ScopeSource is the scope-tree seam: the session's registry of scopes, as
// the dispatch path uses it. Sub-agent scopes fork through this interface
// (plan 07, H4) instead of reaching through the kernel's concrete manager,
// so scope creation is testable without a trace store on disk and the
// concrete type stays in the adapter below and the kernel-facing consumers
// (supervision service, reaper, supervisor tool) that need the full API.
type ScopeSource interface {
	Get(id string) (*goatherd.Scope, bool)
	Create(ownerID string, sb sandbox.Sandbox) (*goatherd.Scope, error)
	Fork(parentID, childOwnerID string, snapshot any) (*goatherd.Scope, error)
	ForkIsolated(parentID, childOwnerID string, snapshot any, sb sandbox.Sandbox) (*goatherd.Scope, error)
	ActiveScopes() []*goatherd.Scope
	Discard(childID string) error

	// Checkpoint operations on a scope, used by the dispatch path's turn
	// checkpointer. On the seam because the checkpointer runs against the
	// same registry the scopes live in.
	CreateCheckpoint(ctx context.Context, scopeID string, snapshot []byte) (*goatherd.Checkpoint, error)
	RestoreCheckpoint(ctx context.Context, checkpointID string) ([]byte, error)
	PruneCheckpoints(ctx context.Context, scopeID string)
}

// kernelTraceSink adapts the kernel's SQLite trace store to TraceSink.
type kernelTraceSink struct {
	store *shepherdtrace.SQLiteTraceStore
}

func (k kernelTraceSink) Append(ctx context.Context, auth shepherdtrace.AppendContext, batch shepherdtrace.AppendBatch) (shepherdtrace.AppendReceipt, error) {
	return k.store.Append(ctx, auth, batch)
}

func (k kernelTraceSink) PublishFrontier(ctx context.Context, auth shepherdtrace.AppendContext, spec shepherdtrace.FrontierSpec) (shepherdtrace.Frontier, error) {
	return k.store.PublishFrontier(ctx, auth, spec)
}

func (k kernelTraceSink) Close(ctx context.Context) error {
	return k.store.Close(ctx)
}

// kernelScopeSource adapts the kernel's scope manager to ScopeSource.
type kernelScopeSource struct{ mgr *goatherd.ScopeManager }

func (k kernelScopeSource) Get(id string) (*goatherd.Scope, bool) { return k.mgr.Get(id) }

func (k kernelScopeSource) Create(ownerID string, sb sandbox.Sandbox) (*goatherd.Scope, error) {
	return k.mgr.Create(ownerID, sb)
}

func (k kernelScopeSource) Fork(parentID, childOwnerID string, snapshot any) (*goatherd.Scope, error) {
	return k.mgr.Fork(parentID, childOwnerID, snapshot)
}

func (k kernelScopeSource) ForkIsolated(parentID, childOwnerID string, snapshot any, sb sandbox.Sandbox) (*goatherd.Scope, error) {
	return k.mgr.ForkIsolated(parentID, childOwnerID, snapshot, sb)
}

func (k kernelScopeSource) ActiveScopes() []*goatherd.Scope { return k.mgr.ActiveScopes() }

func (k kernelScopeSource) Discard(childID string) error { return k.mgr.Discard(childID) }

func (k kernelScopeSource) CreateCheckpoint(ctx context.Context, scopeID string, snapshot []byte) (*goatherd.Checkpoint, error) {
	return k.mgr.CreateCheckpoint(ctx, scopeID, snapshot)
}

func (k kernelScopeSource) RestoreCheckpoint(ctx context.Context, checkpointID string) ([]byte, error) {
	return k.mgr.RestoreCheckpoint(ctx, checkpointID)
}

func (k kernelScopeSource) PruneCheckpoints(ctx context.Context, scopeID string) {
	k.mgr.PruneCheckpoints(ctx, scopeID)
}

// TraceSinkOf adapts a kernel trace store to the seam. A nil store yields a
// nil interface: callers test the seam for nil to mean "tracing is off", so a
// typed nil must never be smuggled across in a non-nil interface value.
func TraceSinkOf(store *shepherdtrace.SQLiteTraceStore) TraceSink {
	if store == nil {
		return nil
	}
	return kernelTraceSink{store: store}
}

// ScopeSourceOf adapts a kernel scope manager to the seam, nil-safely for the
// same reason TraceSinkOf is.
func ScopeSourceOf(mgr *goatherd.ScopeManager) ScopeSource {
	if mgr == nil {
		return nil
	}
	return kernelScopeSource{mgr: mgr}
}

// sinkIsNil reports whether the seam carries no store at all. It exists
// because the composition root still holds the concrete handle: a tracing-off
// session can hand the seam an interface value whose dynamic type is a nil
// *SQLiteTraceStore. `sink == nil` alone would call that "configured" and build
// a live middleware over a nil handle, which is the one way a pure seam swap
// could change behaviour.
func sinkIsNil(sink TraceSink) bool {
	if sink == nil {
		return true
	}
	if k, ok := sink.(kernelTraceSink); ok {
		return k.store == nil
	}
	return false
}

// InitShepherdInfrastructure creates the session-wide Shepherd trace store,
// effect bus, and scope manager. Called once from wiring — tracing is
// integral, so this always runs — and both the orchestrator and sub-agent
// pipelines host shepherd_trace through this shared store. tracePath is
// the store file itself, not its parent directory.
func InitShepherdInfrastructure(tracePath string, busBuffer int) (*shepherdtrace.SQLiteTraceStore, *shepherdtrace.EffectBus, *goatherd.ScopeManager, error) {
	traceDir := filepath.Dir(tracePath)
	// Only tighten the directory we create. tracePath may be relative, in
	// which case traceDir is "." — chmod'ing an existing cwd to 0700 would
	// lock the user out of their own directory.
	_, statErr := os.Stat(traceDir)
	createdDir := os.IsNotExist(statErr)
	if err := os.MkdirAll(traceDir, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("shepherd: mkdir %s: %w", traceDir, err)
	}
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("shepherd: open store: %w", err)
	}
	// Trace data carries prompts and tool-call details. Restrict access to
	// the session owner on POSIX platforms (Windows uses ACLs and ignores
	// POSIX modes; os.Chmod there only toggles the read-only bit).
	if runtime.GOOS != "windows" {
		if createdDir {
			if err := os.Chmod(traceDir, 0o700); err != nil {
				return nil, nil, nil, fmt.Errorf("shepherd: chmod dir %s: %w", traceDir, err)
			}
		}
		if err := os.Chmod(tracePath, 0o600); err != nil {
			return nil, nil, nil, fmt.Errorf("shepherd: chmod store %s: %w", tracePath, err)
		}
	}
	if busBuffer <= 0 {
		busBuffer = defaultBusBuffer
	}
	bus := shepherdtrace.NewEffectBus(busBuffer)
	store.WithBus(bus)
	mgr := goatherd.NewScopeManager(store)
	return store, bus, mgr, nil
}

// CloseShepherdInfrastructure closes the session-wide shepherd trace store
// and its effect bus on a clean session end (plan 07, H2): the store first
// — no more publishes reach the bus — then the bus, whose Close stops its
// subscriber goroutines. Both are idempotent (the store's database Close
// and the bus's closed guard), so a close that already happened — the
// wiring's recovery-failure path closes the store it never hands over —
// is a no-op, not a fabricated failure. Failures warn rather than
// propagate: the clean-exit path reports its teardown failures the same
// way the workspace sandbox destroy does, and its callers have no error
// contract to join into.
func CloseShepherdInfrastructure(store TraceSink, bus *shepherdtrace.EffectBus) {
	if !sinkIsNil(store) {
		if err := store.Close(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: shepherd trace store close: %v\n", err)
		}
	}
	if bus != nil {
		bus.Close()
	}
}
