package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	shepherd "github.com/buchenberg/shepherd-runtime-go"

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

	mgr := shepherd.NewScopeManager(store)

	root, err := mgr.Create("sess-guard", nil)
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	sessionSteer := make(chan string, supervision.SteerBuffer)

	runs := supervision.NewRegistry()
	sup := &SupervisorTool{RootScopeID: root.ID(), ScopeManager: mgr, Runs: runs}

	haltArgs, _ := json.Marshal(map[string]string{
		"action": "halt", "scope_id": root.ID()})
	_, err = sup.Execute(context.Background(), string(haltArgs))
	if err == nil || !strings.Contains(err.Error(), "supervisor_halt_root opt-in") {
		t.Errorf("halt of the root without the gate must refuse with the opt-in message, got: %v", err)
	}

	// Root inject steers the session: delivered to the registered
	// session steering channel, reported as delivery. (Before the halt
	// below — halting discards the root and ends injectable state.)
	runs.Register(root.ID(), supervision.Run{SteerCh: sessionSteer})
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

	// With the gate on, root halt cancels the session's current run:
	// the registered cancellation fires and the halt lands in the
	// kernel (the root scope goes non-active).
	gated := &SupervisorTool{RootScopeID: root.ID(), ScopeManager: mgr, Runs: runs, HaltRoot: true}
	halted := make(chan struct{}, 1)
	haltCtx, ctxCancel := context.WithCancel(context.Background())
	go func() { <-haltCtx.Done(); halted <- struct{}{} }()
	runs.Register(root.ID(), supervision.Run{SteerCh: sessionSteer, Cancel: ctxCancel})
	haltOut, haltErr := gated.Execute(context.Background(), string(haltArgs))
	if haltErr != nil {
		t.Fatalf("gated root halt: %v", haltErr)
	}
	if !strings.Contains(haltOut, "running loop was cancelled") {
		t.Errorf("gated root halt must report the cancelled run, got: %s", haltOut)
	}
	select {
	case <-halted:
	case <-time.After(time.Second):
		t.Fatal("the root halt never invoked the session run cancellation")
	}
	if root.State() == "active" {
		t.Errorf("the kernel-side halt must mark the root non-active, got %s", root.State())
	}

	// An unset RootScopeID (tracing up without a root, or the legacy
	// flat path) guards nothing: a sub-agent scope halts exactly as
	// before, with no gate in the way.
	child, err := mgr.Create("sub-guard", nil)
	if err != nil {
		t.Fatalf("create child scope: %v", err)
	}
	legacy := &SupervisorTool{ScopeManager: mgr, Runs: runs}
	legacyHalt, _ := json.Marshal(map[string]string{
		"action": "halt", "scope_id": child.ID()})
	if _, err := legacy.Execute(context.Background(), string(legacyHalt)); err != nil {
		t.Errorf("a sub-agent halt must not need the root gate, got: %v", err)
	}
}
