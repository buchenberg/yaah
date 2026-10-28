package runner

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
)

// TestDispatchOpts pins the per-dispatch substrate override (yaah-ihfh.3): a
// container world travelling in the context replaces the session's workspace and
// drops the host path validator, and a dispatch without one keeps the captured
// session options exactly as they were.
func TestDispatchOpts(t *testing.T) {
	pv := tools.NewPathValidator("/host/session", false, nil)
	captured := taskRunnerOpts{pathValidator: pv}

	if got := dispatchOpts(captured, context.Background()); got.workspace != nil || got.pathValidator != pv {
		t.Fatalf("a dispatch with no world must keep the session substrate: workspace=%#v validator=%#v", got.workspace, got.pathValidator)
	}

	ws := fakeSandboxWorkspace{}
	got := dispatchOpts(captured, tools.WithDispatchWorkspace(context.Background(), ws))
	if got.workspace != tools.Workspace(ws) {
		t.Errorf("dispatch workspace = %#v, want the per-dispatch world", got.workspace)
	}
	if got.pathValidator != nil {
		t.Error("a per-dispatch world must drop the host path validator — the two substrates must not race")
	}

	// The captured options are the closure's shared state: one dispatch's
	// world must not leak into the next.
	if captured.workspace != nil || captured.pathValidator != pv {
		t.Errorf("the captured options were mutated: workspace=%#v validator=%#v", captured.workspace, captured.pathValidator)
	}
}
