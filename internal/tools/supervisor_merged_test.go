package tools

// The runtime v0.23.0 refusal: Scope.Halt on a merged scope is refused
// (terminal). The halt tool must report that ending to the orchestrator,
// not surface it as an error — nothing is running under a merged scope
// and nothing can be force-stopped.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
)

func TestSupervisorTool_HaltMergedScope_ReportedNotErrored(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })

	mgr := goatherd.NewScopeManager(store)
	root, err := mgr.Create("sess-merged", nil)
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	child, err := mgr.Fork(root.ID(), "sess-merged:child", nil)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if err := mgr.Merge(child.ID()); err != nil {
		t.Fatalf("merge child: %v", err)
	}

	sup := &SupervisorTool{ScopeManager: mgr}
	haltArgs, _ := json.Marshal(map[string]string{
		"action": "halt", "scope_id": child.ID()})
	out, err := sup.Execute(context.Background(), string(haltArgs))
	if err != nil {
		t.Fatalf("halt of a merged scope must report the ending, not error: %v", err)
	}
	if !strings.Contains(out, "already merged") {
		t.Fatalf("expected the merged-report message, got: %s", out)
	}
	// The refusal must not have flipped the scope's state.
	if child.State() != goatherd.ScopeMerged {
		t.Fatalf("refused halt must leave the scope merged, got %s", child.State())
	}
}
