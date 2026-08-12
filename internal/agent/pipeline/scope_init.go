package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// defaultBusBuffer is the per-subscriber effect bus buffer size used when
// the caller passes zero or a negative value.
const defaultBusBuffer = 64

// InitShepherdInfrastructure creates the session-wide Shepherd trace store,
// effect bus, and scope manager. Called once from wiring when
// shepherd_trace_dir is configured — the orchestrator pipeline no longer
// hosts shepherd_trace, so this is the single initialization point shared
// by the SupervisorTool, the supervised_task tool, and sub-agent trace
// middleware.
func InitShepherdInfrastructure(traceDir string, busBuffer int) (*shepherd.SQLiteTraceStore, *shepherd.EffectBus, *shepherd.ScopeManager, error) {
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		return nil, nil, nil, fmt.Errorf("shepherd: mkdir %s: %w", traceDir, err)
	}
	store, err := shepherd.NewSQLiteTraceStore(filepath.Join(traceDir, "trace.sqlite"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("shepherd: open store: %w", err)
	}
	if busBuffer <= 0 {
		busBuffer = defaultBusBuffer
	}
	bus := shepherd.NewEffectBus(busBuffer)
	store.WithBus(bus)
	mgr := shepherd.NewScopeManager(store)
	return store, bus, mgr, nil
}
