package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"

	"github.com/buchenberg/yaah/internal/agent/supervision"
)

// TestSupervisorTool_RootScopeHaltGuarded pins the A1/A2 safety rail:
// halt refuses the session's root scope until the halt bridge lands
// (adoption plan A3, gated by supervisor.halt_root). A halted root would
// silently break every subsequent Fork — the scope manager refuses to
// fork from a non-active parent, and the runner ignores that error,
// leaving later sub-agents with no scope at all. Inject is not guarded
// (A2): the root is registered against the session's steering channel,
// so injecting into it steers the session itself.
func TestSupervisorTool_RootScopeHaltGuarded(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	prevMgr, prevReg := SharedScopeManager, SharedRunRegistry
	SharedScopeManager = shepherd.NewScopeManager(store)
	SharedRunRegistry = supervision.NewRegistry()
	t.Cleanup(func() { SharedScopeManager, SharedRunRegistry = prevMgr, prevReg })

	root, err := SharedScopeManager.Create("sess-guard", nil)
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	sessionSteer := make(chan string, supervision.SteerBuffer)
	SharedRunRegistry.Register(root.ID(), sessionSteer)

	sup := &SupervisorTool{RootScopeID: root.ID()}

	haltArgs, _ := json.Marshal(map[string]string{
		"action": "halt", "scope_id": root.ID()})
	_, err = sup.Execute(context.Background(), string(haltArgs))
	if err == nil || !strings.Contains(err.Error(), "supervisor.halt_root") {
		t.Errorf("halt of the root must refuse with the gated-halt message, got: %v", err)
	}

	// Root inject steers the session: delivered to the registered
	// session steering channel, reported as delivery.
	injectArgs, _ := json.Marshal(map[string]string{
		"action": "inject", "scope_id": root.ID(), "guidance": "wrap up"})
	out, err := sup.Execute(context.Background(), string(injectArgs))
	if err != nil {
		t.Fatalf("inject into the root: %v", err)
	}
	if !strings.Contains(out, "delivered to the running loop") {
		t.Errorf("root inject must report delivery to the session's loop, got: %s", out)
	}
	select {
	case got := <-sessionSteer:
		if got != "wrap up" {
			t.Errorf("session steering got %q, want the guidance verbatim", got)
		}
	default:
		t.Error("root inject never reached the session's steering channel")
	}

	// An unset RootScopeID (tracing up without a root, or the legacy
	// flat path) guards nothing — halt behaves exactly as before.
	legacy := &SupervisorTool{}
	legacyHalt, _ := json.Marshal(map[string]string{
		"action": "halt", "scope_id": root.ID()})
	if _, err := legacy.Execute(context.Background(), string(legacyHalt)); err != nil {
		t.Errorf("without RootScopeID the tool must not guard halt, got: %v", err)
	}
}
