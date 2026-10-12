package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"

	"github.com/buchenberg/yaah/internal/agent/supervision"
)

// TestSupervisorTool_StatusListsFirings pins the B4 surface end to end:
// a rule firing this session shows up in `supervisor status` — rule,
// scope, count, kind — once the firing has been delivered.
func TestSupervisorTool_StatusListsFirings(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	bus := shepherdtrace.NewEffectBus(64)
	t.Cleanup(bus.Close)
	store.WithBus(bus)

	mgr := shepherd.NewScopeManager(store)

	root, err := mgr.Create("sess-status", nil)
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	child, err := mgr.Create("sub-status", nil)
	if err != nil {
		t.Fatalf("create scope: %v", err)
	}

	runs := supervision.NewRegistry()
	steer := make(chan string, supervision.SteerBuffer)
	runs.Register(child.ID(), supervision.Run{SteerCh: steer})

	svc := supervision.NewService(mgr, bus, runs,
		supervision.AdvisoryRules{DestructiveTool: true}, false, false, root.ID())
	svc.Start("status-test")
	t.Cleanup(svc.Close)

	bus.Publish(shepherdtrace.EffectEvent{
		TraceOwnerID: "sub-status",
		Mode:         shepherdtrace.Declaration,
		SchemaRef:    "yaah.tool.bash.v1",
		KindLabel:    "bash",
		Payload:      map[string]any{"cmd": "rm -rf /tmp/probe"},
		Timestamp:    time.Now(),
	})
	select {
	case <-steer:
	case <-time.After(2 * time.Second):
		t.Fatal("the rule firing never reached the steering channel")
	}

	sup := &SupervisorTool{RootScopeID: root.ID(), ScopeManager: mgr, Runs: runs, Svc: svc}
	out, err := sup.Execute(context.Background(), `{"action":"status"}`)
	if err != nil {
		t.Fatalf("Execute(status): %v", err)
	}
	for _, want := range []string{
		"Rule firings:",
		"destructive_file_guard",
		"scope:sub-status",
		"1x (inject)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

// TestSupervisorTool_StatusWithoutService pins the nil-service path:
// with no supervision service, status reports no firings honestly
// instead of guessing.
func TestSupervisorTool_StatusWithoutService(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	mgr := shepherd.NewScopeManager(store)

	sup := &SupervisorTool{ScopeManager: mgr, Runs: supervision.NewRegistry()}
	out, err := sup.Execute(context.Background(), `{"action":"status"}`)
	if err != nil {
		t.Fatalf("Execute(status): %v", err)
	}
	if !strings.Contains(out, "Rule firings: 0 (supervision service not active)") {
		t.Errorf("status without a service must say so, got:\n%s", out)
	}
}
