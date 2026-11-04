package tools

import "testing"

// TestInFlightDispatchScopesTrackLiveSet pins the yaah-95ri.6 in-flight
// bookkeeping: container/worktree dispatch scopes are marked in flight
// from fork to teardown so the reaper's live set (via
// LiveDispatchScopeIDs) spares them however long the dispatch runs.
func TestInFlightDispatchScopesTrackLiveSet(t *testing.T) {
	rt := RetainedRuntime{inFlight: newInFlightScopes()}
	if got := rt.InFlightDispatchScopeIDs(); len(got) != 0 {
		t.Fatalf("zero-value runtime: InFlightDispatchScopeIDs = %v, want empty", got)
	}

	rt.BeginDispatchScope("scope:dispatch-1")
	rt.BeginDispatchScope("scope:dispatch-2")
	if got := rt.InFlightDispatchScopeIDs(); len(got) != 2 {
		t.Fatalf("two in-flight dispatches: got %v, want 2 IDs", got)
	}

	rt.EndDispatchScope("scope:dispatch-1")
	if got := rt.InFlightDispatchScopeIDs(); len(got) != 1 {
		t.Fatalf("after ending one: got %v, want 1 ID", got)
	}

	rt.EndDispatchScope("scope:dispatch-2")
	rt.EndDispatchScope("scope:dispatch-2") // idempotent
	if got := rt.InFlightDispatchScopeIDs(); len(got) != 0 {
		t.Fatalf("after ending both: got %v, want empty", got)
	}
}
