package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"

	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// haltBlockProvider blocks every model call until its context is
// cancelled — the wedged sub-agent the supervisor needs to halt.
type haltBlockProvider struct{}

func (p *haltBlockProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestTaskRunner_HaltBridgeCancelsTheRun pins the A3 runner side end to
// end: a dispatch registers under its scope with a cancellation; a
// supervisor Cancel through the registry stops the wedged run; the
// dispatch reports "halted by supervisor" (a deliberate stop, not raw
// context-canceled noise); and the registry entry is gone when the run
// ends, so a later inject or halt reports "no active run".
func TestTaskRunner_HaltBridgeCancelsTheRun(t *testing.T) {
	initTestRoles(t)

	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	mgr := shepherd.NewScopeManager(store)

	root, err := CreateRootScope(mgr, "sess-halt", "", true) // pure-causal root
	if err != nil {
		t.Fatalf("CreateRootScope: %v", err)
	}

	runs := supervision.NewRegistry()
	opts := taskRunnerOpts{
		provider:     &haltBlockProvider{},
		systemPrompt: "BASE",
		modelName:    "test-model",
		subCfg:       config.SubAgentConfig{},
		defaults:     config.Defaults{},
		rootScopeID:  root.ID(),
		runs:         runs,
		scopeManager: mgr,
		traceStore:   store,
	}
	runner := makeTaskRunner(opts, 0)

	type runResult struct {
		result string
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, rerr := runner(context.Background(), "do the thing", tools.SubAgentParams{Role: "developer"})
		done <- runResult{result, rerr}
	}()

	// Wait for the dispatch to register its scope under the root, then
	// halt it through the registry — the supervisor's path.
	var scopeID string
	deadline := time.After(5 * time.Second)
	for {
		ids := runs.ScopeIDs()
		if len(ids) > 0 {
			scopeID = ids[0]
			break
		}
		select {
		case <-deadline:
			t.Fatal("the dispatch never registered its run")
		case <-done:
			t.Fatal("the wedged run ended before it could be halted")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if _, err := runs.Cancel(scopeID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case res := <-done:
		if res.err == nil || !strings.Contains(res.err.Error(), "halted by supervisor") {
			t.Errorf("halted run error = %v, want the deliberate \"halted by supervisor\" wording", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the halted run never returned")
	}

	// The bridge entry is cut: a later inject or halt at the scope is
	// honestly refused, never delivered to the dead run.
	if _, err := runs.Cancel(scopeID); err != supervision.ErrNoRun {
		t.Errorf("Cancel after the run ended = %v, want ErrNoRun", err)
	}
}
