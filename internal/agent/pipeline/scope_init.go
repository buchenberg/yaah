package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
)

// defaultBusBuffer is the per-subscriber effect bus buffer size used when
// the caller passes zero or a negative value.
const defaultBusBuffer = 64

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
func CloseShepherdInfrastructure(store *shepherdtrace.SQLiteTraceStore, bus *shepherdtrace.EffectBus) {
	if store != nil {
		if err := store.Close(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: shepherd trace store close: %v\n", err)
		}
	}
	if bus != nil {
		bus.Close()
	}
}
