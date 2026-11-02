package yaah

import (
	"context"
	"testing"
)

// A workspace-access ask is raised inside a tool call of the active run, so it
// must carry that run's context: the permission prompt is what a sub-agent or an
// ACP client can sit on for the full timeout after the turn is abandoned.
func TestPromptWorkspaceAccessChainsRunContext(t *testing.T) {
	type ctxKey struct{}

	runCtx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "run-1"))
	defer cancel()

	var got context.Context
	s := &agentSession{runCtx: runCtx}
	s.approveCallFn = func(ctx context.Context, callID, name, args string) bool {
		got = ctx
		return false
	}

	if s.promptWorkspaceAccess("C:/elsewhere/file.txt", "outside the workspace") {
		t.Fatal("want the deny from the approval bridge")
	}
	if got == nil {
		t.Fatal("the approval bridge was never called")
	}
	if v, _ := got.Value(ctxKey{}).(string); v != "run-1" {
		t.Fatalf("approval ctx lost the run context: value = %q", v)
	}
	cancel()
	if got.Err() == nil {
		t.Fatal("approval ctx is not cancelled when the run is")
	}
}

// With no run in flight there is nothing to chain, and the ask must not fail —
// it falls back to a plain context so an approval can still be raised.
func TestPromptWorkspaceAccessWithoutRunUsesPlainContext(t *testing.T) {
	var got context.Context
	s := &agentSession{}
	s.approveCallFn = func(ctx context.Context, callID, name, args string) bool {
		got = ctx
		return false
	}

	if s.promptWorkspaceAccess("C:/elsewhere/file.txt", "outside the workspace") {
		t.Fatal("want the deny from the approval bridge")
	}
	if got == nil {
		t.Fatal("the approval bridge was never called")
	}
	if err := got.Err(); err != nil {
		t.Fatalf("fallback ctx is already done: %v", err)
	}
}
