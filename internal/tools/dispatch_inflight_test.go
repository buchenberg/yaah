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

	rt.BeginDispatchScope("dispatch-1") // raw owner-id form
	if got := rt.InFlightDispatchScopeIDs(); len(got) != 1 || got[0] != "scope:dispatch-1" {
		t.Fatalf("raw id must be keyed in kernel form (scope:<id>), got %v", got)
	}

	// The teardown path ends with scope.ID() — the kernel form. Begin and
	// End must meet (yaah-95ri.6 review: the ID-form mismatch made every
	// entry leak and the spare ineffective).
	rt.EndDispatchScope("scope:dispatch-1")
	if got := rt.InFlightDispatchScopeIDs(); len(got) != 0 {
		t.Fatalf("kernel-form end must clear a raw-form begin: got %v", got)
	}

	rt.BeginDispatchScope("scope:dispatch-2")
	rt.EndDispatchScope("scope:dispatch-2") // idempotent
	if got := rt.InFlightDispatchScopeIDs(); len(got) != 0 {
		t.Fatalf("after ending both: got %v, want empty", got)
	}
}
