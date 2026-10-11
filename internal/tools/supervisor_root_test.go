package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
)

// TestSupervisorTool_RootScopeGuarded pins the A1 safety rail: inject and
// halt refuse the session's root scope until the execution bridge lands
// (adoption plan A2/A3). Today both verbs are trace-side only, and a
// halted root would silently break every subsequent Fork — the scope
// manager refuses to fork from a non-active parent, and the runner ignores
// that error, leaving later sub-agents with no scope at all.
func TestSupervisorTool_RootScopeGuarded(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	prevMgr := SharedScopeManager
	SharedScopeManager = shepherd.NewScopeManager(store)
	t.Cleanup(func() { SharedScopeManager = prevMgr })

	root, err := SharedScopeManager.Create("sess-guard", nil)
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	child, err := SharedScopeManager.Create("sub-guard", nil)
	if err != nil {
		t.Fatalf("create child scope: %v", err)
	}

	sup := &SupervisorTool{RootScopeID: root.ID()}

	injectArgs, _ := json.Marshal(map[string]string{
		"action": "inject", "scope_id": root.ID(), "guidance": "steer"})
	_, err = sup.Execute(context.Background(), string(injectArgs))
	if err == nil || !strings.Contains(err.Error(), "session root scope") {
		t.Errorf("inject into the root must refuse with the bridge message, got: %v", err)
	}

	haltArgs, _ := json.Marshal(map[string]string{
		"action": "halt", "scope_id": root.ID()})
	_, err = sup.Execute(context.Background(), string(haltArgs))
	if err == nil || !strings.Contains(err.Error(), "supervisor.halt_root") {
		t.Errorf("halt of the root must refuse with the gated-halt message, got: %v", err)
	}

	// A sub-agent scope keeps the pre-A1 trace-side behavior: inject and
	// halt still succeed (their execution bridge is A2/A3).
	injectChild, _ := json.Marshal(map[string]string{
		"action": "inject", "scope_id": child.ID(), "guidance": "steer"})
	if _, err := sup.Execute(context.Background(), string(injectChild)); err != nil {
		t.Errorf("inject into a sub-agent scope must keep working: %v", err)
	}
	haltChild, _ := json.Marshal(map[string]string{
		"action": "halt", "scope_id": child.ID()})
	if _, err := sup.Execute(context.Background(), string(haltChild)); err != nil {
		t.Errorf("halt of a sub-agent scope must keep working: %v", err)
	}

	// An unset RootScopeID (tracing up without a root, or the legacy
	// flat path) guards nothing — the tool behaves exactly as before.
	legacy := &SupervisorTool{}
	legacyInject, _ := json.Marshal(map[string]string{
		"action": "inject", "scope_id": root.ID(), "guidance": "steer"})
	if _, err := legacy.Execute(context.Background(), string(legacyInject)); err != nil {
		t.Errorf("without RootScopeID the tool must not guard, got: %v", err)
	}
}
