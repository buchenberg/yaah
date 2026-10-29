package tools

import (
	"context"
	"testing"
)

// TestDispatchWorkspaceContext pins the per-dispatch override's plumbing: a
// workspace travels in the context (nil is a no-op, a child context inherits),
// so the retained lane can hand a container world to the runner without either
// package importing the other.
func TestDispatchWorkspaceContext(t *testing.T) {
	base := context.Background()
	if ws := DispatchWorkspace(base); ws != nil {
		t.Fatalf("a bare context carries no dispatch workspace, got %#v", ws)
	}

	if got := WithDispatchWorkspace(base, nil); got != base {
		t.Error("a nil workspace must be a no-op, not a new context")
	}

	ws := Workspace(newLocalWorkspace(nil))
	ctx := WithDispatchWorkspace(base, ws)
	if got := DispatchWorkspace(ctx); got != ws {
		t.Fatalf("DispatchWorkspace = %#v, want the workspace it carried", got)
	}

	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if got := DispatchWorkspace(child); got != ws {
		t.Error("a child context must inherit the dispatch workspace")
	}

	// An explicit override wins: a nested dispatch must be able to hand its
	// own world down without the parent's leaking through.
	other := Workspace(newLocalWorkspace(NewPathValidator("/other", false, nil)))
	if got := DispatchWorkspace(WithDispatchWorkspace(ctx, other)); got != other {
		t.Error("a nearer dispatch workspace must override the inherited one")
	}
}
