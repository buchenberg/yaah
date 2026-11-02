package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"github.com/buchenberg/goatherd-runtime/sandbox/containment"
)

// unconfinableBackend reports itself available and then refuses at probe time.
//
// This is what a host does when it can mint a restricted token but cannot make
// the write restriction grant-conformant: `winacl`'s per-root conformance check
// fails on the writable root itself ("DENIES writes (profile too strict)"), so
// every launch dies even though the availability check passed. A dispatch lane
// in this state loses every execution tool at once — the bug behind yaah-1tkp.2.
type unconfinableBackend struct{}

func (unconfinableBackend) Name() string              { return "unconfinable" }
func (unconfinableBackend) EnforcementTier() string   { return "paddock" }
func (unconfinableBackend) Available() (bool, string) { return true, "restricted token" }
func (unconfinableBackend) SupportsNetwork(confinement.NetMode) bool {
	return true
}
func (unconfinableBackend) ProfileFor([]string, confinement.NetworkPolicy) string {
	return "profile"
}
func (unconfinableBackend) Probe(string, string, []string) error {
	return fmt.Errorf("%w: writable root %s DENIES writes (profile too strict)",
		containment.ErrPaddockNotEstablished, "C:/world")
}
func (unconfinableBackend) Launch(string, containment.LaunchRequest) (containment.LaunchResult, error) {
	return containment.LaunchResult{}, nil
}

// echoWorkspaceHealth runs the workspace's own shell printing a marker, so a
// test asserts the fallback actually executed a command rather than a no-op.
func echoWorkspaceHealth(t *testing.T, ws *localWorkspace) ExecResult {
	t.Helper()
	shell, flag := ws.Shell()
	res, err := ws.Exec(context.Background(), ExecRequest{Command: shell, Args: []string{flag, "echo advisory"}})
	if err != nil {
		t.Fatalf("Exec returned an error, want the advisory fallback to run: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "advisory") {
		t.Fatalf("advisory fallback result = %+v, want exit 0 and the marker in stdout", res)
	}
	return res
}

// containment: auto is documented as "advisory, recorded" when enforcement
// cannot be established. A probe that fails *after* the availability check used
// to bypass that contract entirely and fail every command in the lane.
func TestConfinementDegrade_AutoFallsBackAndRecords(t *testing.T) {
	ws := confinedTestWorkspace(t.TempDir(), unconfinableBackend{}, containment.PlacementAuto)

	if reason, ok := WorkspaceDegraded(ws); ok || reason != "" {
		t.Fatalf("degraded before any command ran: %q", reason)
	}

	echoWorkspaceHealth(t, ws)

	reason, ok := WorkspaceDegraded(ws)
	if !ok {
		t.Fatal("auto placement lost its jail but reported no downgrade")
	}
	if !strings.Contains(reason, "DENIES writes") {
		t.Fatalf("recorded reason = %q, want the backend's own explanation", reason)
	}
}

// The record is one-time and shared across the per-call clones of the
// workspace, so a lane does not re-log the same condition per tool call.
func TestConfinementDegrade_RecordedOnceAcrossClones(t *testing.T) {
	ws := confinedTestWorkspace(t.TempDir(), unconfinableBackend{}, containment.PlacementAuto)
	echoWorkspaceHealth(t, ws)

	first, _ := WorkspaceDegraded(ws)

	// WithEnv clones the struct but not the pointer's target, so the record is
	// shared rather than restarted.
	clone := ws.WithEnv([]string{"YAAH_TEST=1"})
	second, ok := WorkspaceDegraded(clone)
	if !ok || second != first {
		t.Fatalf("clone reason = %q ok=%v, want the original %q", second, ok, first)
	}
}

// paddock is the explicit "jail or refuse" placement: it must keep failing
// closed, and the message must name the fix.
func TestConfinementDegrade_PaddockStillFailsClosed(t *testing.T) {
	ws := confinedTestWorkspace(t.TempDir(), unconfinableBackend{}, containment.PlacementPaddock)

	_, err := ws.Exec(context.Background(), ExecRequest{Command: "echo", Args: []string{"nope"}})
	if err == nil {
		t.Fatal("paddock placement must refuse when confinement cannot be established")
	}
	if !strings.Contains(err.Error(), "agents.subagent.containment: auto") {
		t.Fatalf("error = %v, want it to name the remedy", err)
	}
	if _, ok := WorkspaceDegraded(ws); ok {
		t.Fatal("paddock must not record a downgrade")
	}
}

// A declared grant set is a promise the unpoliced host path cannot keep, so
// narrowing disables the fallback even under auto.
func TestConfinementDegrade_NarrowedGrantsStillFail(t *testing.T) {
	root := t.TempDir()
	ws := confinedTestWorkspace(root, unconfinableBackend{}, containment.PlacementAuto).
		WithWritableRoots([]string{root})

	if _, err := ws.Exec(context.Background(), ExecRequest{Command: "echo", Args: []string{"nope"}}); err == nil {
		t.Fatal("a narrowed grant set must not silently run unconfined")
	}
	if _, ok := WorkspaceDegraded(ws); ok {
		t.Fatal("narrowed grants must not record a downgrade")
	}
}

// Egress rules live in the jail, so degrading would silently drop them.
func TestConfinementDegrade_NetworkRuleStillFails(t *testing.T) {
	for _, mode := range []confinement.NetMode{confinement.NetDenyAll, confinement.NetBroker} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			pv := NewPathValidator(root, false, nil)
			pv.WorkDir = pv.WorkspaceRoot
			ws := NewConfinedLocalWorkspace(pv, unconfinableBackend{}, containment.PlacementAuto,
				confinement.NetworkPolicy{Mode: mode})

			if _, err := ws.Exec(context.Background(), ExecRequest{Command: "echo", Args: []string{"nope"}}); err == nil {
				t.Fatalf("network mode %q must not degrade to an unpoliced host path", mode)
			}
			if _, ok := WorkspaceDegraded(ws); ok {
				t.Fatalf("network mode %q must not record a downgrade", mode)
			}
		})
	}
}

// A workspace that never asked for confinement reports no downgrade, so callers
// can distinguish "advisory by design" from "lost its jail".
func TestConfinementDegrade_UnconfinedWorkspaceReportsNothing(t *testing.T) {
	ws := newLocalWorkspace(NewPathValidator(t.TempDir(), false, nil))
	if reason, ok := WorkspaceDegraded(ws); ok || reason != "" {
		t.Fatalf("unconfined workspace reported a downgrade: %q", reason)
	}
}
