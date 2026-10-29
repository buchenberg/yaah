package yaah

import (
	"context"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
)

// TestSessionLoopBuilderWiresTraceStore pins the composition seam the
// orchestrator trace depends on: the session's shared store must reach
// LoopConfig, not only the sub-agent runner options.
func TestSessionLoopBuilderWiresTraceStore(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close(context.Background())

	s := &agentSession{cfg: &config.Config{}, traceStore: store, sessionID: "sess-wired"}
	loop := s.loopBuilder(nil, "test-model", nil, "", nil, "", "").Build(agent.LoopBuildOptions{})
	if loop.Config.TraceStore != store {
		t.Fatal("session loop builder dropped the shared trace store")
	}
}
