package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	shepherd "github.com/buchenberg/shepherd-runtime-go"

	"github.com/buchenberg/yaah/internal/agent/supervision"
)

// TestSupervisorTool_InjectBridgesToRun pins the A2 supervisor-tool side:
// with the run registry active, inject both records the trace fact and
// delivers to the running loop; without a registered run the outcome is
// reported honestly instead of claiming delivery; status reports the
// bridge.
func TestSupervisorTool_InjectBridgesToRun(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	mgr := shepherd.NewScopeManager(store)
	runs := supervision.NewRegistry()

	child, err := mgr.Create("sub-bridge", nil)
	if err != nil {
		t.Fatalf("create scope: %v", err)
	}
	steerCh := make(chan string, supervision.SteerBuffer)
	runs.Register(child.ID(), supervision.Run{SteerCh: steerCh})

	sup := &SupervisorTool{ScopeManager: mgr, Runs: runs}
	injectArgs, _ := json.Marshal(map[string]string{
		"action": "inject", "scope_id": child.ID(), "guidance": "narrow the scope"})
	out, err := sup.Execute(context.Background(), string(injectArgs))
	if err != nil {
		t.Fatalf("Execute(inject): %v", err)
	}
	if !strings.Contains(out, "delivered to the running loop") {
		t.Errorf("inject with a registered run must report delivery, got: %s", out)
	}
	select {
	case got := <-steerCh:
		if got != "narrow the scope" {
			t.Errorf("delivered guidance = %q, want it verbatim", got)
		}
	default:
		t.Error("the guidance never reached the run's steering channel")
	}

	// A scope with no registered run: the trace fact is recorded, the
	// result says so — never "delivered".
	idle, err := mgr.Create("sub-idle", nil)
	if err != nil {
		t.Fatalf("create idle scope: %v", err)
	}
	idleArgs, _ := json.Marshal(map[string]string{
		"action": "inject", "scope_id": idle.ID(), "guidance": "hello"})
	out, err = sup.Execute(context.Background(), string(idleArgs))
	if err != nil {
		t.Fatalf("Execute(inject, idle): %v", err)
	}
	if !strings.Contains(out, "no active run") {
		t.Errorf("inject without a run must say so, got: %s", out)
	}

	// A wedged run (buffer full) surfaces the busy error, not a hang.
	wedged, err := mgr.Create("sub-wedged", nil)
	if err != nil {
		t.Fatalf("create wedged scope: %v", err)
	}
	runs.Register(wedged.ID(), supervision.Run{SteerCh: make(chan string, 1)})
	wedgedArgs, _ := json.Marshal(map[string]string{
		"action": "inject", "scope_id": wedged.ID(), "guidance": "a"})
	wedgedArgs2, _ := json.Marshal(map[string]string{
		"action": "inject", "scope_id": wedged.ID(), "guidance": "b"})
	if _, err := sup.Execute(context.Background(), string(wedgedArgs)); err != nil {
		t.Fatalf("first inject into wedged: %v", err)
	}
	out, err = sup.Execute(context.Background(), string(wedgedArgs2))
	if err != nil {
		t.Fatalf("busy inject must not be a hard error (the trace holds the intervention): %v", err)
	}
	if !strings.Contains(out, "steering buffer is full") {
		t.Errorf("busy inject must explain the undelivered guidance, got: %s", out)
	}

	// Status reports the bridge.
	statusArgs, _ := json.Marshal(map[string]string{"action": "status"})
	out, err = sup.Execute(context.Background(), string(statusArgs))
	if err != nil {
		t.Fatalf("Execute(status): %v", err)
	}
	if !strings.Contains(out, "Bridged runs: 2") {
		t.Errorf("status must report the two registered runs, got: %s", out)
	}

	// Unregister and the run is honestly gone: inject degrades to the
	// trace-only outcome without an error.
	runs.Unregister(child.ID())
	out, err = sup.Execute(context.Background(), string(injectArgs))
	if err != nil {
		t.Fatalf("Execute(inject, after unregister): %v", err)
	}
	if !strings.Contains(out, "no active run") {
		t.Errorf("inject after the run finished must say so, got: %s", out)
	}
}
